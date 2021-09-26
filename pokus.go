package main
// vim: set et sw=4 :

import (
    "fmt"
    "os"
    "time"
)


func main() {
    t := time.Now()
    mod, err := NewEthToolModule(os.Args[1])
    d := time.Since(t)
    if err != nil { panic(err) }
    fmt.Printf("[%.4fs] Module type: %d eeprom len: %d\n", d.Seconds(), mod.tpe, mod.eeprom_len)

    t = time.Now()
    txd, err := mod.TxrDiag()
    d = time.Since(t)
    if err != nil { panic(err) }

    fmt.Printf("[%.4fs] %.2fC %.4fV %.3fmA %.4fmW(%.2fdBm) %.4fmW(%.2fdBm)\n", d.Seconds(),
        txd.temperature_C,
        txd.voltage_V,
        txd.bias_mA,
        txd.transmit_mW,
        txd.transmit_dBm,
        txd.receive_mW,
        txd.receive_dBm,
    )

    t = time.Now()
    tags, err := mod.ModuleInfo(TXR_MI_ALLOW_CACHE)
    d = time.Since(t)
    if err != nil { panic(err) }
    fmt.Printf("[%.4fs]", d.Seconds())
    for k, v := range(tags) {
      fmt.Printf(" %s='%s'", k, v)
    }
    fmt.Printf("\n")

    t = time.Now()
    tags, err = mod.ModuleInfo(TXR_MI_ALLOW_CACHE)
    d = time.Since(t)
    if err != nil { panic(err) }
    fmt.Printf("[%.4fs]", d.Seconds())
    for k, v := range(tags) {
      fmt.Printf(" %s='%s'", k, v)
    }
    fmt.Printf("\n")
}
