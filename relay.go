package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-ble/ble"
	"github.com/go-ble/ble/examples/lib/dev"
	"github.com/pkg/errors"
	"gopkg.in/ini.v1"
)

// constants of the characteristic UUIDs
const (
	environmentUUID = "181a"
	temperatureUUID = "2a1f"
	humidityUUID    = "2a6f"

	batteryServiceUUID = "180f"
	batteryLevelUUID   = "2a19"

	atcDeviceRegex  = `ATC_[0-9A-Z]+`
	macAddressRegex = `([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}`
)

var (
	device = flag.String("device", "default", "implementation of ble")

	scanDuration = flag.Duration("sd", 5*time.Second, "scanning duration, 0 for indefinitely")

	deviceSettingsFile = flag.String("device-settings", "devices.ini", "device settings file")

	mqttHost     = flag.String("mqtt-host", "localhost", "MQTT host")
	mqttPort     = flag.Int("mqtt-port", 1883, "MQTT port")
	mqttClientId = flag.String("mqtt-client-id", "atc-sensor-relay", "MQTT client ID")
)

func isAtcDevice(a ble.Advertisement) bool {
	// check if the device's name matches the ATC regex
	atcRegex := regexp.MustCompile(atcDeviceRegex)
	return atcRegex.MatchString(a.LocalName())
}

// sensor settings structure
type sensorInfo struct {
	sensorName string
	mqttTopic  string
}

func loadKnownSensors(settingsFilePath string) map[string]sensorInfo {
	// parse an ini file like this
	/*
		$ cat ./devices.ini
		[A4:C1:38:0C:5B:45]
		sensorname=edge of desk
		topic=temperature/room

		[...next device...]
	*/

	// read the ini file
	iniFile, err := ini.Load(settingsFilePath)
	if err != nil {
		log.Fatalf("can't load device settings file: %s", err)
	}

	knownSensors := make(map[string]sensorInfo)

	addressMatcher := regexp.MustCompile(macAddressRegex)
	atcMatcher := regexp.MustCompile(atcDeviceRegex)

	// iterate over known sensors
	for _, section := range iniFile.Sections() {

		addressOrName := section.Name()

		if !addressMatcher.MatchString(strings.ToLower(addressOrName)) && !atcMatcher.MatchString(addressOrName) {
			continue
		}

		// get the sensor name
		sensorName, err := section.GetKey("sensorname")
		if err != nil {
			log.Fatalf("can't get sensor name: %s", err)
		}

		// get the MQTT topic
		mqttTopic, err := section.GetKey("topic")
		if err != nil {
			log.Fatalf("can't get MQTT topic: %s", err)
		}

		// add the sensor to the known sensors map
		knownSensors[strings.ToLower(addressOrName)] = sensorInfo{
			sensorName: strings.ToLower(sensorName.String()),
			mqttTopic:  mqttTopic.String(),
		}
	}

	return knownSensors
}

func main() {

	// read known sensor mapping
	knownSensors := loadKnownSensors(*deviceSettingsFile)
	fmt.Printf("Known sensors: %v\n", knownSensors)

	if len(knownSensors) == 0 {
		fmt.Printf("no known sensors, exiting...\n")
		return
	}

	flag.Parse()

	d, err := dev.NewDevice(*device)
	if err != nil {
		log.Fatalf("can't new device : %s", err)
	}
	ble.SetDefaultDevice(d)

	foundDevices := make(map[string]sensorInfo)

	// Print the results map
	for key, value := range foundDevices {
		fmt.Printf("ID: %s, %s\n", key, value)
	}

	scanContext := ble.WithSigHandler(context.WithTimeout(context.Background(), *scanDuration))

	// Scan for specified durantion, or until interrupted by user.
	fmt.Printf("Scanning for %s...\n", *scanDuration)
	// Scan for specified durantion, or until interrupted by user.
	chkErr(ble.Scan(scanContext, false, func(a ble.Advertisement) {
		deviceAddress := strings.ToLower(a.Addr().String())
		deviceLocalName := a.LocalName()

		// check if either the address or local name is in the known devices
		if info, ok := knownSensors[deviceAddress]; ok {
			foundDevices[deviceAddress] = info
		} else if info, ok := knownSensors[deviceLocalName]; ok {
			foundDevices[deviceLocalName] = info
		} else {
			return
		}
	}, nil))

	if len(foundDevices) == 0 {
		fmt.Printf("No devices found, exiting...\n")
		return
	} else {
		fmt.Printf("%d devices found:\n", len(foundDevices))
		for id, info := range foundDevices {
			fmt.Printf("ID: %s, %s\n", id, info.sensorName)
		}
	}

	numConnections := len(foundDevices)
	fmt.Printf("starting loop for %d devices\n", numConnections)

	// topic -> payload
	payloads := make(map[string]interface{})
	for deviceName, info := range foundDevices {
		payload, err := getDeviceData(deviceName, info)
		if err == nil {
			fmt.Printf("got: %v\n", payload)
			topic := knownSensors[deviceName].mqttTopic
			payloads[topic] = payload
		} else {
			fmt.Printf("Error polling device %s: %s\n", deviceName, err)
		}
	}

	fmt.Printf("got %d payloads\n", len(payloads))

	if len(payloads) > 0 {
		var messagePubHandler mqtt.MessageHandler = func(client mqtt.Client, msg mqtt.Message) {
			fmt.Printf("[MQTT] Received message: %s from topic: %s\n", msg.Payload(), msg.Topic())
		}

		var connectHandler mqtt.OnConnectHandler = func(client mqtt.Client) {
			fmt.Println("[MQTT] Connected")
		}

		var connectLostHandler mqtt.ConnectionLostHandler = func(client mqtt.Client, err error) {
			fmt.Printf("[MQTT] Connect lost: %v", err)
		}

		opts := mqtt.NewClientOptions()
		opts.AddBroker(fmt.Sprintf("tcp://%s:%d", *mqttHost, *mqttPort))
		opts.SetClientID(*mqttClientId)
		opts.SetDefaultPublishHandler(messagePubHandler)
		opts.OnConnect = connectHandler
		opts.OnConnectionLost = connectLostHandler
		fmt.Printf("[MQTT] connecting to host: %s\n", *mqttHost)
		client := mqtt.NewClient(opts)
		if token := client.Connect(); token.Wait() && token.Error() != nil {
			panic(token.Error())
		}

		for topic, payload := range payloads {
			jsonPayload, err := json.Marshal(payload)
			if err != nil {
				log.Fatalf("[MQTT] can't marshal payload: %s", err)
			}

			fmt.Printf("[MQTT] Sending payload: %s\n", jsonPayload)

			// Publish the JSON payload to a topic
			token := client.Publish(topic, 0, false, jsonPayload)
			token.Wait()
		}

		client.Disconnect(1000)
	}

	fmt.Println("All connections have finished.")
}

func getDeviceData(nameOrAddress string, info sensorInfo) (map[string]interface{}, error) {
	fmt.Printf("Connecting to %s...\n", nameOrAddress)
	filter := func(a ble.Advertisement) bool {
		return a.LocalName() == nameOrAddress || a.Addr().String() == nameOrAddress
	}
	serviceDiscoveryContext := ble.WithSigHandler(context.WithTimeout(context.Background(), 60*time.Second))
	cln, err := ble.Connect(serviceDiscoveryContext, filter)
	if err != nil {
		log.Fatalf("failed to connect to %s: %s", nameOrAddress, err)
		return nil, err
	}

	// Make sure we had the chance to print out the message.
	done := make(chan struct{})
	// Normally, the connection is disconnected by us after our exploration.
	// However, it can be asynchronously disconnected by the remote peripheral.
	// So we wait(detect) the disconnection in the go routine.
	go func() {
		<-cln.Disconnected()
		fmt.Printf("[ %s ] is disconnected \n", cln.Addr())
		close(done)
	}()

	fmt.Printf("Discovering profile for device %s...\n", cln.Addr())
	p, err := cln.DiscoverProfile(true)
	if err != nil {
		log.Fatalf("can't discover profile: %s", err)
	}

	// Start the exploration.
	payload := readCharacteristics(cln, p)
	payload["address"] = cln.Addr().String()
	payload["sensorname"] = info.sensorName
	fmt.Printf("Got payload: %v\n", payload)

	// Disconnect the connection. (On OS X, this might take a while.)
	fmt.Printf("Disconnecting [ %s ]\n", cln.Addr())
	cln.CancelConnection()
	<-done

	return payload, nil
}

func parseLittleEndianValue(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

func readCharacteristics(cln ble.Client, p *ble.Profile) map[string]interface{} {
	payload := make(map[string]interface{})
	for _, s := range p.Services {

		// we are only interested in the environment sensing service and possibly the battery service
		if s.UUID.String() != environmentUUID && s.UUID.String() != batteryServiceUUID {
			continue
		}

		// fmt.Printf("    Service: %s %s, Handle (0x%02X)\n", s.UUID, ble.Name(s.UUID), s.Handle)
		for _, c := range s.Characteristics {

			// fmt.Printf("      Characteristic: %s %s, Property: 0x%02X (%s), Handle(0x%02X), VHandle(0x%02X), Value(%s)\n",
			// 	c.UUID, ble.Name(c.UUID), c.Property, propString(c.Property), c.Handle, c.ValueHandle, c.Value)

			if (c.Property & ble.CharRead) != 0 {
				b, err := cln.ReadCharacteristic(c)
				if err != nil {
					fmt.Printf("Failed to read characteristic: %s\n", err)
					continue
				}
				switch c.UUID.String() {
				// environment sensing service
				case temperatureUUID:
					payload["temperature"] = float32(parseLittleEndianValue(b)) / 10

				case humidityUUID:
					payload["humidity"] = parseLittleEndianValue(b) / 100
				}
			}
		}

		payload["timestamp"] = time.Now().UnixMilli() / 1000.0
	}

	return payload
}

func propString(p ble.Property) string {
	var s string
	for k, v := range map[ble.Property]string{
		ble.CharBroadcast:   "B",
		ble.CharRead:        "R",
		ble.CharWriteNR:     "w",
		ble.CharWrite:       "W",
		ble.CharNotify:      "N",
		ble.CharIndicate:    "I",
		ble.CharSignedWrite: "S",
		ble.CharExtended:    "E",
	} {
		if p&k != 0 {
			s += v
		}
	}
	return s
}

func chkErr(err error) {
	switch errors.Cause(err) {
	case nil:
	case context.DeadlineExceeded:
		fmt.Printf("done\n")
	case context.Canceled:
		fmt.Printf("canceled\n")
	default:
		log.Fatalf(err.Error())
	}
}
