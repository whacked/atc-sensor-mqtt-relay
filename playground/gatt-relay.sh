#!/usr/bin/env bash

error=0
[ -z "$DEVICE_SETTINGS_FILE" ] && echo "DEVICE_SETTINGS FILE is not set" && error=1
[ -z "$BROKER_HOST" ] && echo "BROKER_HOST is not set" && error=1
[ $error -eq 1 ] && exit 1


broker_host=$BROKER_HOST
broker_port=${BROKER_PORT-1883}
device_settings_file=$DEVICE_SETTINGS_FILE

# example settings file
# 
# [A4:C1:38:0C:AA:01]
# sensorname=first location or name or whatever
# topic=environment/sensors/room1/position2
# ;
# [A4:C1:38:05:BC:DE]
# sensorname=another placement
# topic=environment/sensors/room3/position5

output_to_hex() {
	cat - | cut -d: -f3 | tr ' ' "\n" | tac | grep -v '^$' | paste -d "" -s - | tr '[:lower:]' '[:upper:]'
}
parse_hex() {
	echo "ibase=16; $(cat -)" | bc
}

# device_addr=A4:C1:38:8B:E2:71
device_addr=
sensorname=
topic=

handle_device() {
	device_addr=$1
}

while IFS= read -r line; do
	if [[ $line == ";"* ]]; then
		continue
	fi
	if [[ $line == "" ]]; then
		device_addr=
		sensorname=
		topic=
	fi
	if [[ $line == "["* ]]; then
		device_addr=$(echo $line | tr -d '[]')
		continue
	fi

	if [ "x$device_addr" != "x" ]; then
		case $line in
			sensorname=*)
				sensorname="$(echo $line | cut -d= -f2)"
				;;
			topic=*)
				topic="$(echo $line | cut -d= -f2)"
				;;
		esac
		if [ "x$sensorname" != "x" ] && [ "x$topic" != "x" ]; then
			echo "PROCESSING $device_addr / $sensorname / $topic"

			# temperature
			temperature=$(gatttool -b $device_addr --char-read --uuid='0x2a1f' | output_to_hex | parse_hex | (echo "scale=1; $(cat -) / 10") | bc -l)

			# humidity
			humidity=$(gatttool -b $device_addr --char-read --uuid='0x2a6f' | output_to_hex | parse_hex | (echo "scale=0; $(cat -) / 100") | bc -l)

			if [ "x$temperature" != "x" ] && [ "x$humidity" != "x" ]; then
				timestamp=$(date +%s)
				payload=$(echo '{"address":"'$device_addr'","sensorname":"'$sensorname'","temperature":'$temperature',"humidity":'$humidity',"timestamp":'$timestamp'}')
				# echo $payload | tee -a /tmp/out.log
				echo $payload | jq

				mosquitto_pub -h $broker_host -p $broker_port -t "$topic" -m "$payload"
			fi

			temperature=
			humidity=
		fi
	fi

done < "$device_settings_file"


