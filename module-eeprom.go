package main
// vim: set et sw=4 :

import (
    "fmt"
    "encoding/binary"
    "errors"
    "math"
    "unsafe"
    "golang.org/x/sys/unix"
)

const (
    TXR_MI_ALLOW_CACHE = 0x7FFF
    TXR_MI_ALL         = 0x3FFF

    TXR_MI_VENDOR   = 1 << 0
    TXR_MI_OUI      = 1 << 1
    TXR_MI_PRODUCT  = 1 << 2
    TXR_MI_REVISION = 1 << 3
    TXR_MI_WAVELEN  = 1 << 4
    TXR_MI_SERIAL   = 1 << 5
    TXR_MI_DATE     = 1 << 6
)

type EthToolModule struct {
    ifname     [unix.IFNAMSIZ]byte
    tpe        uint32
    eeprom_len uint32
}

type TranscieverDiagnostics struct {
    temperature_C float64
    voltage_V     float64
    bias_mA       float64
    transmit_mW   float64
    receive_mW    float64
    transmit_dBm  float64
    receive_dBm   float64
}

var ethtool_socket int = -1

func CloseEthToolSocket() {
    if ethtool_socket >= 0 {
        unix.Close(ethtool_socket)
        ethtool_socket = -1
    }
}

type ifreq struct {
    ifr_name [unix.IFNAMSIZ]byte
    ifr_data uintptr
}

func ethtool(ifname [unix.IFNAMSIZ]byte, data uintptr) error {
    if ethtool_socket < 0 {
        fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_IP)
        if err != nil {
            return err
        }
        ethtool_socket = fd
    }

    ifr := ifreq{
        ifr_name: ifname,
        ifr_data: data,
    }

    _, _, ep := unix.Syscall(unix.SYS_IOCTL, uintptr(ethtool_socket), unix.SIOCETHTOOL, uintptr(unsafe.Pointer(&ifr)))
    if ep != 0 {
        return ep
    }

    return nil
}

type ethtoolModInfo struct {
    cmd        uint32
    tpe        uint32
    eeprom_len uint32
    reserved   [8]uint32
}

func NewEthToolModule(ifname string) (*EthToolModule, error) {
    var name [unix.IFNAMSIZ]byte
    copy(name[:], []byte(ifname))
    modInfo := ethtoolModInfo{cmd: unix.ETHTOOL_GMODULEINFO}
    err := ethtool(name, uintptr(unsafe.Pointer(&modInfo)))
    if err != nil {
        return nil, err
    }
    return &EthToolModule{
        ifname:     name,
        tpe:        modInfo.tpe,
        eeprom_len: modInfo.eeprom_len,
    }, nil
}

const (
    ETH_MODULE_SFF_8472 = 0x2
    ETH_MODULE_SFF_8472_LEN = 512
)


type ethtoolEeprom struct {
    cmd    uint32
    magic  uint32
    offset uint32
    len    uint32
    data   [ETH_MODULE_SFF_8472_LEN]byte
}

func (e *EthToolModule) Read(offset uint32, len uint32) ([]byte, error) {
    if e.eeprom_len < 1 {
        return nil, errors.New("ethtool: No EEPROM to read.")
    }
    if offset > e.eeprom_len {
        return nil, errors.New("ethtool: Offset out of bounds.")
    }
    if offset == e.eeprom_len {
        return nil, nil
    }
    if e.eeprom_len - offset < len {
        len = e.eeprom_len - offset
    }
    eeprom := ethtoolEeprom{
        cmd: unix.ETHTOOL_GMODULEEEPROM,
        offset: offset,
        len: len,
    }
    err := ethtool(e.ifname, uintptr(unsafe.Pointer(&eeprom)))
    if err != nil { return nil, err }
    return eeprom.data[:len], nil
}

const (
    txr_MULT_C  = 1.0/256.0
    txr_MULT_V  = 1.0/10000.0
    txr_MULT_mA = 1.0/500.0
    txr_MULT_mW = 1.0/10000.0
)

func (e *EthToolModule) TxrDiag() (*TranscieverDiagnostics, error) {
    if e.tpe != ETH_MODULE_SFF_8472 {
        return nil, fmt.Errorf("Unsupported module type: %v", e.tpe)
    }
/*
    ethtool -m enp129s0f0 offset 0x160 length 10
    Offset          Values
    ------          ------
    0x0160:         27 09 80 79 0b 5d 14 ce 16 02 
                    TT TT VV VV CC CC OO OO RR RR

    network endianity
    TT TT temperature                           in   1/256 C  (0.00390625 C)
    VV VV Module voltage                        in 1/10000 V  (0.0001 V)
    CC CC Laser bias current                    in  2/1000 A  (2 mA)
    OO OO Laser output power                    in 1/10000 mW (0.0001 mW);  dBm = log(mW)/log(10)*10
    RR RR Receiver signal average optical power in 1/10000 mw (0.0001 mW);  dBm = log(mW)/log(10)*10
*/

    data, err := e.Read(0x160, 10)
    if err != nil { return nil, err }
    var w [5]float64
    for i := 0; i < 5; i++ {
        w[i] = float64(binary.BigEndian.Uint16(data[i*2:i*2+2]))
    }
    tx := w[3] * txr_MULT_mW
    rx := w[4] * txr_MULT_mW
    return &TranscieverDiagnostics {
        temperature_C: w[0] * txr_MULT_C,
        voltage_V:     w[1] * txr_MULT_V,
        bias_mA:       w[2] * txr_MULT_mA,
        transmit_mW:   tx,
        receive_mW:    rx,
        transmit_dBm:  math.Log10(tx)*10.0,
        receive_dBm:   math.Log10(rx)*10.0,
    }, nil
}

const (
    txr_DECODE_STRING = iota
    txr_DECODE_INT
    txr_DECODE_OUI
)

type eepromEntryDef struct {
    name    string
    offset  uint32
    length  uint32
    flag    int
    decoder int
}

func fromLatin1(bytes []byte) (string) {
    l := len(bytes)
    output := make([]rune, l)
    lastchar := 0
    for i, b := range(bytes) {
        if (b != 0 && b != 0x20) {
            lastchar = i + 1
        }
        output[i] = rune(b)
    }
    return string(output[:lastchar])
}

func validSerial(sn string) bool {
    other_chars := 0
    alnum := 0
    for _, r := range(sn) {
        if r < ' ' || r > '~' {
            other_chars ++
        } else if ( r >= '0' && r <= '9' ) || ( r >= 'A' && r <= 'Z') || ( r <= 'a' && r >= 'z' ) {
            alnum ++
        }
    }
    return alnum > 3 && other_chars == 0
}

const GAP_MERGE = 4 // merge reads with gap of at most this size between them
const infty = 0xffff

var txrEepromStatic = [...]eepromEntryDef{
    // Must be sorted by offset
    { name: "vendor",    offset: 0x14,  length: 16, flag: TXR_MI_VENDOR,   decoder: txr_DECODE_STRING, },
    { name: "oui",       offset: 0x25,  length: 3,  flag: TXR_MI_OUI,      decoder: txr_DECODE_OUI,    },
    { name: "product",   offset: 0x28,  length: 16, flag: TXR_MI_PRODUCT,  decoder: txr_DECODE_STRING, },
    { name: "revision",  offset: 0x38,  length: 4,  flag: TXR_MI_REVISION, decoder: txr_DECODE_STRING, },
    { name: "wavelen",   offset: 0x3c,  length: 2,  flag: TXR_MI_WAVELEN,  decoder: txr_DECODE_INT,    },
    { name: "serial",    offset: 0x44,  length: 16, flag: TXR_MI_SERIAL,   decoder: txr_DECODE_STRING, },
    { name: "mfgdate",   offset: 0x54,  length: 8,  flag: TXR_MI_WAVELEN,  decoder: txr_DECODE_STRING, },
    { name: "--last--",  offset: infty, length: 0,  flag: 0,               decoder: 0},
}

func GetTxrInfoFlags(str []string) (int, error) {
    ret := 0
    for _, info := range(str) {
        switch info {
            case "ALL":
                ret = ret | TXR_MI_ALL
            case "CACHE":
                ret = ret | TXR_MI_ALLOW_CACHE
            default:
                found := false
                for _, def := range(txrEepromStatic) {
                    if info == def.name {
                        found = true
                        ret = ret | def.flag
                    }
                }
                if !found {
                    return 0, fmt.Errorf("Unknown entry '%s'", info)
                }
        }
    }
    return ret, nil
}

type bufferInfo struct {
    def     int
    buf_pos uint32
}

func decodeStatic(buf []byte, decoder int) string {
    switch decoder {
        case txr_DECODE_STRING:
            return fromLatin1(buf)
        case txr_DECODE_OUI:
            return fmt.Sprintf("%02x:%02x:%02x",buf[0], buf[1], buf[2])
        case txr_DECODE_INT:
            acc := 0
            for _, d := range(buf) {
                acc = 256 * acc + int(d)
            }
            return fmt.Sprintf("%d", acc)
        default:
            panic("Invalid eeprom definition")
    }
}

func (e *EthToolModule) moduleInfo(flags int) (map[string]string, error) {
    if e.tpe != ETH_MODULE_SFF_8472 {
        return nil, fmt.Errorf("Unsupported module type: %v", e.tpe)
    }
    ret := make(map[string]string)
    query := make([]bufferInfo, len(txrEepromStatic))
    var query_start uint32 = 0
    var query_end   uint32 = 0
    query_len   := 0
    for i, qdef := range(txrEepromStatic) {
        // fmt.Printf("Outer loop[%d] %s (offset:0x%02x)\n", i, qdef.name, qdef.offset)
        if query_len > 0 && query_end < qdef.offset - GAP_MERGE {
            // fmt.Printf("  Querying: query_len:%d query_start:0x%02x query_end:0x%02x\n", query_len, query_start, query_end)
            buf, err := e.Read(query_start, query_end - query_start)
            if err != nil { return nil, err }
            for j:=0; j<query_len; j++ {
                ddef    := txrEepromStatic[query[j].def]
                buf_pos := query[j].buf_pos
                buf_end := buf_pos + ddef.length
                // fmt.Printf("  Decoding query[%d] name:%s offset:0x%02x len:0x%02x buf_pos:0x%02x buf_end:0x%02x decoder:%d\n",
                //              j, ddef.name, ddef.offset, ddef.length, buf_pos, buf_end, ddef.decoder)
                ret[ddef.name] = decodeStatic(buf[buf_pos:buf_end], ddef.decoder)
                // fmt.Printf("    ->'%s'\n",ret[ddef.name])
            }
            query_len = 0
        }
        if qdef.flag & flags != 0 {
            if query_len == 0 {
                query_start = qdef.offset
            }
            query[query_len].buf_pos = qdef.offset - query_start
            query[query_len].def = i
            // fmt.Printf("  Adding query[%d] %s offset:0x%02x len:0x%02x at [%d] with buf_pos:0x%02x\n",
            //           i, qdef.name, qdef.offset, qdef.length, query_len, query[query_len].buf_pos)
            query_len ++
            query_end = qdef.offset + qdef.length
        }
    }
    //fmt.Printf("RET:")
    //for k, v := range(ret) { fmt.Printf(" %s:'%s'", k, v) }
    //fmt.Printf("\n")
    return ret, nil
}

var moduleCache = make(map[string]map[string]string)

func (e *EthToolModule) ModuleInfo(flags int) (map[string]string, error) {
    var sn string
    have_sn := false
    if flags == TXR_MI_ALLOW_CACHE {
        serial, err := e.moduleInfo(TXR_MI_SERIAL)
        if (err != nil) { return nil, err }
        sn, have_sn = serial["serial"]
        if have_sn && validSerial(sn) {
            if ret, found := moduleCache[sn]; found {
                return ret, nil
            }
        }
    }
    if have_sn {
        flags = flags &^ TXR_MI_SERIAL
    }
    ret, err := e.moduleInfo(flags)
    if (err != nil) { return nil, err }
    if have_sn {
        // this is TXR_MI_ALLOW_CACHE branch
        ret["serial"] = sn
        retcopy := make(map[string]string)
        for k, v := range ret {
            retcopy[k] = v
        }
        moduleCache[sn] = retcopy
    }
    return ret, nil
}
