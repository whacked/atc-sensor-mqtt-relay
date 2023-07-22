this is basically a simplified version of [MiTemperature2](https://github.com/JsBergbau/MiTemperature2) using go, primarily targeting something like the raspberry pi for reading from a set of mijia hygrometers flashed with the [ATC_MiThermometer](https://github.com/atc1441/ATC_MiThermometer) firmware.

the settings ini file and the payload structure loosely follows that of MiTemperature2. it scans for devices and sends payloads to an mqtt broker.


# quickstart

build + run

```
go build
sudo ./relay ...
```

# building for raspberry pi

env GOOS=linux GOARCH=arm go build -o rpi-relay relay.go
