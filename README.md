Ethtool Exporter
================

This exporter uses ethtool syscall to collect transciever diagnosis from
optical ethernet cards.  It does currently support only type
ETH\_MODULE\_SFF\_8472 (0x2). It was tested only with `ixbge` network cards, so
current default search path only include devices using this driver.

It has endpoints `/metrics` for prometheus and `/influx` for scraping by
telegraph.

Influx format includes also dBm values for laser input and output power,
they are simply calulated from mW values provided by the transciever.

Implementation
--------------

`ethtool` utility reads whole eeprom, which is slow (~0.1s per transciever just
reading the memory).  So I reimplemented `ethtool -m` reading and parsing in
go, with following optimizations:
  * Tags are cached by serial number of transciever, on each scraping is read
    only serial number (16 bytes) and other tag values are read only first time,
    then they are filled from cache.
  * No alert limits for metrics are read, just the metrics themselves (10 bytes)
