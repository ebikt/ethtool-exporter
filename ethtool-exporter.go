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
    "sync"
    "time"

    "github.com/mpvl/unique"
    "github.com/prometheus/common/expfmt"
    "github.com/prometheus/common/version"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// {{{ prometheus vars
const namespace = "ethtool"

// transcieverFullLabels[2:] are names of tags obtained by EthToolModule.ModuleInfo()
var transcieverFullLabels = []string{"iface","error","vendor","revision","product","serial","wavelen","mfgdate"}
var transcieverLabels     = []string{"iface"}

var (
    transciever_present = prometheus.NewDesc(
        prometheus.BuildFQName(namespace, "", "transciever_present"),
        "Scrape of transciever was successfull",
        transcieverFullLabels, nil,
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
    parallel     *regexp.Regexp
}

func NewExporter(pathGlob []string, debug bool, parallel *regexp.Regexp) (*Exporter, error) {
    flagList := make([]string, len(transcieverFullLabels)-1)
    copy(flagList[1:], transcieverFullLabels[2:])
    // CACHE would be sufficient, the other entries are just for validating that we get them back
    flagList[0] = "CACHE"
    flags, err := GetTxrInfoFlags(flagList)
    if err != nil { return nil, err }
    return &Exporter{
        pathGlob:     pathGlob,
        txrInfoFlags: flags,
        debug:        debug,
        parallel:     parallel,
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

type Emiter interface {
    Emit(iface string, err error, tags map[string]string, metrics *TranscieverDiagnostics)
}
type MetricChan chan<- prometheus.Metric
type InfluxChan chan<- string

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
    e.DiscoverAndCollect(MetricChan(ch))
}

func (e *Exporter) DiscoverAndCollect(ch Emiter) {
    ifaces, err := e.GetIfaces()
    if (err != nil) {
        panic(err)
    }
    parallel := make(map[string][]string)
    for _, iface := range(ifaces) {
        groups := e.parallel.FindStringSubmatch(iface)
        var key string
        if groups == nil {
            key = "\x01!nil!"
        } else {
            key = strings.Join(groups[1:], "\x02")
        }
        values, found := parallel[key]
        if (found) {
            values = append(values, iface)
        } else {
            values = []string{iface}
        }
        parallel[key] = values
    }
    if (len(parallel) < 2) {
        e.CollectIfacesSerially(ifaces, ch)
    } else {
        var waitGroup sync.WaitGroup
        for _, series := range(parallel) {
            if e.debug {
                fmt.Printf("Collecting %v\n", series)
            }
            waitGroup.Add(1)
            go func (s... string) {
                defer waitGroup.Done()
                e.CollectIfacesSerially(s, ch)
            } (series...)
        }
        waitGroup.Wait()
    }
}

func (e *Exporter) CollectIfacesSerially(ifaces []string, ch Emiter) {
    for _, iface := range(ifaces) {
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
        ch.Emit(iface, err, tags, metrics)
    }
}



func (ch MetricChan)Emit(iface string, err error, tags map[string]string, metrics *TranscieverDiagnostics) {
    labels := make([]string, len(transcieverFullLabels))
    for i, label := range(transcieverFullLabels) {
        switch label {
            case "error": if err != nil { labels[i] = err.Error() }
            case "iface": labels[i] = iface
            default:
                labels[i] = tags[label]
        }
    }
    if err == nil {
        ch <- prometheus.MustNewConstMetric(transciever_present, prometheus.GaugeValue, 1, labels...)
        ch <- prometheus.MustNewConstMetric(transciever_temp, prometheus.GaugeValue, metrics.temperature_C,       iface)
        ch <- prometheus.MustNewConstMetric(transciever_volt, prometheus.GaugeValue, metrics.voltage_V,           iface)
        ch <- prometheus.MustNewConstMetric(transciever_bias, prometheus.GaugeValue, metrics.bias_mA     * 0.001, iface)
        ch <- prometheus.MustNewConstMetric(transciever_txw,  prometheus.GaugeValue, metrics.transmit_mW * 0.001, iface)
        ch <- prometheus.MustNewConstMetric(transciever_rxw,  prometheus.GaugeValue, metrics.receive_mW  * 0.001, iface)
    } else {
        ch <- prometheus.MustNewConstMetric(transciever_present, prometheus.GaugeValue, 0, labels...)
    }
}

func (ch InfluxChan)Emit(iface string, err error, tags map[string]string, metrics *TranscieverDiagnostics) {
    tagList := make([]string, 0, len(transcieverFullLabels))
    for _, label := range(transcieverFullLabels) {
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
        ch <- fmt.Sprintf("%v_transciever,%v present=1i,temperature_C=%.2f,voltage_V=%.3f,bias_A=%.6f,receive_power_dBm=%.2f,transmit_power_dBm=%.2f,receive_power_W=%.7f,transmit_power_W=%.7f",
                    namespace, tagStr,
                    metrics.temperature_C, metrics.voltage_V, metrics.bias_mA * 0.001,
                    metrics.receive_dBm, metrics.transmit_dBm, metrics.receive_mW * 0.001, metrics.transmit_mW * 0.001,
              )
    } else {
        ch <- fmt.Sprintf("%v_transciever,%v present=0i\n",
                          namespace, tagStr)
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
    
    now := time.Now()
    nowi := now.UnixNano()
    lines := make(chan string)
    go func () {
        e.DiscoverAndCollect(InfluxChan(lines))
        lines <- "\x00EOF"
    } ()

    for line := <-lines; line != "\x00EOF"; line =  <-lines {
        fmt.Fprintf(writer, "%s %v\n", line, nowi)
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
        parallel = flag.String("parallel", "^(.*)$", "regular expression that matches inteface name - " +
                        "Interfaces that differ in capture groups are collected in parallel.\n" +
                        "I.e. \"^(.*)\" means full parallel, \"^(.*[^0-9])\" means enp1s2f0 and enp1s2f1\n" +
                        " are collected in series but parallel with another series enp1s3f0 and enp1s3f1.",
                   )
        pathGlob arrayFlags
        defaultPath = []string { "/sys/bus/pci/drivers/ixgbe/*:*/net/*" }
    )
    flag.Var(&pathGlob, "devices",
        "Shell glob that enumerate network devices to scrap. Repeatable.\n" + 
        "Last component must resolve to name of network device. Default: " + strings.Join(defaultPath, ", "),
    )
    flag.Parse()
    if len(pathGlob) == 0 {
        pathGlob = defaultPath
    }

    exporter, err := NewExporter(pathGlob, *debug, regexp.MustCompile(*parallel))
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
