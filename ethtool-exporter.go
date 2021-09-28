package main
// vim: set et sw=4 :

import (
    "flag"
    "fmt"
    "io"
    "net/http"
    "regexp"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "time"

    "github.com/mpvl/unique"
    "github.com/prometheus/common/expfmt"
    "github.com/prometheus/common/version"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// {{{ prometheus vars
const namespace = "ethtool"

// transcieverErrLabels[2:] are names of tags obtained by EthToolModule.ModuleInfo()
var transcieverErrLabels = []string{"error","iface","vendor","revision","product","serial","wavelen","mfgdate"}
var transcieverLabels    = transcieverErrLabels[1:]

var (
    transciever_present = prometheus.NewDesc(
        prometheus.BuildFQName(namespace, "", "transciever_present"),
        "Scrape of transciever was successfull",
        transcieverErrLabels, nil,
    )
    transciever_temp = prometheus.NewDesc(
        prometheus.BuildFQName(namespace, "", "transciever_temp"),
        "Transciever temperature (C)",
        transcieverLabels, nil,
    )
    transciever_volt = prometheus.NewDesc(
        prometheus.BuildFQName(namespace, "", "transciever_volt"),
        "Transciever voltage (V)",
        transcieverLabels, nil,
    )
    transciever_bias = prometheus.NewDesc(
        prometheus.BuildFQName(namespace, "", "transciever_bias"),
        "Laser bias current (A)",
        transcieverLabels, nil,
    )
    transciever_txw = prometheus.NewDesc(
        prometheus.BuildFQName(namespace, "", "transciever_txw"),
        "Laser output power (W)",
        transcieverLabels, nil,
    )
    transciever_rxw = prometheus.NewDesc(
        prometheus.BuildFQName(namespace, "", "transciever_rxw"),
        "Receiver signal average optical power (W)",
        transcieverLabels, nil,
    )
)
// }}}

type Exporter struct { // {{{
    pathGlob     []string
    debug        bool
    txrInfoFlags int
}

func NewExporter(pathGlob []string, debug bool) (*Exporter, error) {
    flagList := make([]string, len(transcieverLabels))
    copy(flagList[1:], transcieverLabels[1:])
    // CACHE would be sufficient, the other entries are just for validating that we get them back
    flagList[0] = "CACHE"
    flags, err := GetTxrInfoFlags(flagList)
    if err != nil { return nil, err }
    return &Exporter{
        pathGlob:     pathGlob,
        txrInfoFlags: flags,
        debug:        debug,
    }, nil
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
    ch <- transciever_present
    ch <- transciever_temp
    ch <- transciever_volt
    ch <- transciever_bias
    ch <- transciever_txw
    ch <- transciever_rxw
}

func (e *Exporter) GetIfaces() ([]string, error) {
    var ret []string
    for _, glob := range(e.pathGlob) {
        matches, err := filepath.Glob(glob)
        if e.debug {
            fmt.Printf("GetIfaces() %v -> %v\n", glob, matches)
        }
        if err != nil { return nil, err }
        for _, match := range(matches) {
            slash := strings.LastIndex(match, "/")
            ret = append(ret, match[slash+1:]) // works also for no "/" as slash == -1
        }
    }
    sort.Strings(ret)
    unique.Strings(&ret)
    return ret, nil
}

func gaugeValue(desc *prometheus.Desc, value float64, labels []string) prometheus.Metric {
    return prometheus.MustNewConstMetric(
        desc, prometheus.GaugeValue, value, labels...
    )
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
    ifaces, err := e.GetIfaces()
    if (err != nil) {
        panic(err)
    }
    for _, iface := range ifaces {
        m, err  := NewEthToolModule(iface)
        var metrics *TranscieverDiagnostics
        var tags    map[string]string
        if err == nil {
            tags, err = m.ModuleInfo(e.txrInfoFlags)
        } else {
            tags = make(map[string]string)
        }
        if err == nil {
            metrics, err = m.TxrDiag()
        }
        labels := make([]string, len(transcieverErrLabels))
        for i, label := range(transcieverErrLabels) {
            switch label {
                case "error": if err != nil { labels[i] = err.Error() }
                case "iface": labels[i] = iface
                default:
                    labels[i] = tags[label]
            }
        }
        if err == nil {
            ch <- gaugeValue(transciever_present, 1,                         labels)
            ch <- gaugeValue(transciever_temp, metrics.temperature_C,        labels[1:])
            ch <- gaugeValue(transciever_volt, metrics.voltage_V,            labels[1:])
            ch <- gaugeValue(transciever_bias, metrics.bias_mA     * 0.001, labels[1:])
            ch <- gaugeValue(transciever_txw,  metrics.transmit_mW * 0.001, labels[1:])
            ch <- gaugeValue(transciever_rxw,  metrics.receive_mW  * 0.001, labels[1:])
        } else {
            ch <- gaugeValue(transciever_present, 0, labels)
        }
    }
}

var (
    // Replace various quotes and backslashes in original text
    // with '~' sign,
    // influxdb is not consistent with itself when parsing quotes
    // and when parsing consecutive backslashes.
    dangerousChars = regexp.MustCompile("[\\\"'`]")
    // Escape comma and equal sign
    escapeChars    = regexp.MustCompile("([,=])")
    // Replace control characters and space by escaped space
    whiteChars     = regexp.MustCompile("[[:cntrl:][:space:]]")
)

func (e *Exporter) Influxdb(writer io.Writer) {
    ifaces, err := e.GetIfaces()
    if (err != nil) {
        panic(err)
    }
    now := time.Now()
    nowi := now.UnixNano()
    for _, iface := range ifaces {
        m, err := NewEthToolModule(iface)
        var metrics *TranscieverDiagnostics
        var tags    map[string]string
        if err == nil {
            tags, err = m.ModuleInfo(e.txrInfoFlags)
        } else {
            tags = make(map[string]string)
        }
        if err == nil {
            metrics, err = m.TxrDiag()
        }
        tagList := make([]string, 0, len(transcieverErrLabels))
        for _, label := range(transcieverErrLabels) {
            var value string
            switch label {
                case "iface": value = iface
                case "error": if (err != nil) { value = err.Error() }
                default: value = tags[label]
            }
            if len(value)>0 {
                value = dangerousChars.ReplaceAllString(value, "~")
                value = whiteChars.ReplaceAllString(value, "\\ ")
                value = escapeChars.ReplaceAllString(value, "\\$1")
                tagList = append(tagList, fmt.Sprintf("%s=%v", label, value))
            }
        }
        tagStr := strings.Join(tagList, ",")
        if err == nil {
            fmt.Fprintf(writer, "%v_transciever,%v present=1i,temperature_C=%.2f,voltage_V=%.3f,bias_A=%.6f,receive_power_dBm=%.2f,transmit_power_dBm=%.2f,receive_power_W=%.7f,transmit_power_W=%.7f %v\n",
                        namespace, tagStr,
                        metrics.temperature_C, metrics.voltage_V, metrics.bias_mA * 0.001,
                        metrics.receive_dBm, metrics.transmit_dBm, metrics.receive_mW * 0.001, metrics.transmit_mW * 0.001,
                        nowi)
        } else {
            fmt.Fprintf(writer, "%v_transciever,%v present=0i %v\n",
                        namespace, tagStr, nowi)
        }
    }
}

func (e *Exporter) InfluxHandler() (func(http.ResponseWriter, *http.Request)) {
    return func(w http.ResponseWriter, _ *http.Request) {
        e.Influxdb(w)
    }
}
// }}}

type arrayFlags []string // {{{
func (a *arrayFlags) String() string {
    return strings.Join(*a, ", ")
}
func (a *arrayFlags) Set(value string) error {
    *a = append(*a, value)
    return nil
}
// }}

func main() { // {{{
    var (
        test     = flag.Bool("test", false, "test run - gather methrics and print them")
        influx   = flag.Bool("test-influx", false, "single run - gather methrics and print them in influx line format")
        addr     = flag.String("web.listen-address", "127.0.0.1:9992", "The address to listen on for HTTP requests.")
        debug    = flag.Bool("debug", false, "test run with debug printing (currently only iface glob match)")
        pathGlob arrayFlags
        defaultPath = []string { "/sys/bus/pci/drivers/ixgbe/*:*/net/*" }
    )
    flag.Var(&pathGlob, "devices",
        "Shell glob that enumerate network devices to scrap. Repeatable. Last component must resolve to name of network device. Default: " + strings.Join(defaultPath, ", "),
    )
    flag.Parse()
    if len(pathGlob) == 0 {
        pathGlob = defaultPath
    }

    exporter, err := NewExporter(pathGlob, *debug)
    if err != nil { panic(err) }
    if _, err := exporter.GetIfaces(); err != nil {
        panic(err)
    }

    if *influx {
        exporter.Influxdb(os.Stdout);
        os.Exit(0);
        return
    }

    prometheus.MustRegister(exporter)
    prometheus.MustRegister(version.NewCollector(namespace))

    if *test || *debug {
        // Run full prometheus gather and print to stdout
        gth := prometheus.DefaultGatherer
        mfs, err := gth.Gather()
        enc := expfmt.NewEncoder(os.Stdout, expfmt.FmtText)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        }
        for _, mf := range mfs {
            err = enc.Encode(mf)
            if err != nil {
                fmt.Fprintf(os.Stderr, "Error: %v\n", err)
            }
        }
        return
    } else {
        http.Handle("/metrics", promhttp.Handler())
        http.HandleFunc("/influx", exporter.InfluxHandler())
        http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
            w.Write([]byte(`<html>
  <head><title>NetHW Exporter</title></head>
  <body><h1>NetHW Exporter</h1>
  <p><a href="/metrics">Metrics</a></p>
  <p><a href="/influx">Metrics in influxdb format</a></p>
</html>
`))
        })
        err := http.ListenAndServe(*addr, nil)
        if (err != nil) {
            fmt.Fprintf(os.Stderr, "Error: %v\n", err)
            os.Exit(1)
        }
    }
} // }}}
