package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stianeikeland/go-rpio/v4"
)

const configFilePath = "config.json"

var config FortinoConfig
var mqttClient mqtt.Client

type FortinoConfig struct {
	MQTT           MQTTConfig
	UpdateInterval int                   `json:"update_interval"`
	DigitalOutputs []DigitalOutputConfig `json:"outputs"`
	Thermostat     ThermostatConfig      `json:"thermostat"`
	Onewires       []OneWireSensor       `json:"onewire"`
}

type MQTTConfig struct {
	Topic     string
	Host      string
	Port      int
	KeepAlive int
	Username  string
	Password  string
}

type DigitalOutputConfig struct {
	Name          string
	Type          string
	PIN           byte
	InvertedLogic bool `json:"inverted_logic"`
	State         bool `json:"initial"`
}

type ThermostatConfig struct {
	Enabled      bool    `json:"enabled"`
	Setpoint     float64 `json:"setpoint"`
	Actuator     string
	FeedbackType string `json:"feedback_type"`
	FeedbackName string `json:"feedback_name"`
	Regulator    string
	Hysteresis   float64
	Runtime      uint `json:"runtime"`
}

type OneWireSensor struct {
	Name string
	ID   string
	Type string
}

type SensorSample struct {
	Name      string
	Type      string
	Status    string
	fValue    float64
	iValue    int
	SampledAt time.Time
}

func SensorSamplingRuutine(samplingInterval int) {

	for {

		jsonObj := map[string]interface{}{}
		jsonObj["Time"] = time.Now().UTC().Format("2006-01-02T15:04:05")

		// DS18B20...
		ds18BIndex := 1
		for _, s := range config.Onewires {

			if s.Type != "DS18B20" {
				continue
			}

			temp, err := ReadTemp_DS18B20(s.ID)
			if err != nil {
				continue
			}

			var betterID string
			minusIndex := strings.Index(s.ID, "-")
			if minusIndex > 0 {
				betterID = s.ID[(minusIndex + 1):len(s.ID)]
			} else {
				betterID = s.ID
			}

			// JSON Object update
			jsonObj[fmt.Sprintf("DS18B20-%d", ds18BIndex)] = map[string]interface{}{
				"Id":          strings.ToUpper(betterID),
				"Temperature": float64(int(temp*100)) / 100.0,
			}
			ds18BIndex = ds18BIndex + 1
		}

		jsonStr, err := json.Marshal(jsonObj)
		if err != nil {
			log.Fatal(err)
		}
		topic := fmt.Sprintf("tele/%s/SENSOR", config.MQTT.Topic)
		mqttClient.Publish(topic, 0, false, jsonStr)

		time.Sleep(time.Second * time.Duration(samplingInterval))
	}
}

func ReadTemp_DS18B20(ID string) (float64, error) {

	w1BusPath := fmt.Sprintf("/sys/bus/w1/devices/%s/w1_slave", ID)
	dat, err := os.ReadFile(w1BusPath)
	if err != nil {
		log.Printf("%s", string(err.Error()))
		return -10000, err
	}

	lines := strings.Split(string(dat), "\n")
	if len(lines) < 2 {
		log.Println("invalid DS18B20 bus payload")
		return -10010, errors.New("invalid DS18B20 bus payload")
	}

	if !strings.HasSuffix(strings.TrimRight(lines[0], "\r\n"), "YES") {
		log.Println("invalid DS18B20 CRC")
		return -10020, errors.New("invalid DS18B20 CRC")
	}

	if strings.Count(lines[1], "=") != 1 {
		log.Println("invalid DS18B20 format")
		return -10030, errors.New("invalid DS18B20 fortmat")
	}

	equalSignIndex := strings.Index(lines[1], "=")
	if equalSignIndex > 0 {
		tempInMilliC := lines[1][(equalSignIndex + 1):len(lines[1])]
		tempInMilli, err := strconv.Atoi(tempInMilliC)
		if err != nil {
			return -10040, err
		}
		return float64(tempInMilli) / 1000.0, nil
	}

	panic("counted 1 equal sign, found none")
}

var mqttCallback mqtt.MessageHandler = func(client mqtt.Client, msg mqtt.Message) {

	lowerTopic := strings.ToLower(msg.Topic())

	if strings.HasSuffix(lowerTopic, "/temptargetset") {
		rawFloat := string(msg.Payload())

		if s, err := strconv.ParseFloat(rawFloat, 64); err == nil {
			config.Thermostat.Setpoint = s
			log.Printf("TEMPTARGETSET Thermostat set point set to %.1f", config.Thermostat.Setpoint)
		} else {
			log.Printf("TEMPTARGETSET Failed to parse temp setpoint '%s'", rawFloat)
		}
	} else {
		fmt.Printf("TOPIC: %s\n", msg.Topic())
		fmt.Printf("MSG: %s\n", msg.Payload())
	}
}

func ThermostatRoutine(th *ThermostatConfig) {

	if config.Thermostat.Hysteresis < 0 {
		log.Fatal("thermostat hysteresis can not be negative")
	}

	var feedbackSensorID string
	for _, s := range config.Onewires {
		if s.Name == th.FeedbackName {
			feedbackSensorID = s.ID
		}
	}
	if len(feedbackSensorID) == 0 {
		log.Printf("invalid thermostat feedback %s", th.FeedbackName)
		return
	}

	heaterState := false

	time.Sleep(time.Second * 10)
	runtime := (time.Second * time.Duration(th.Runtime))

	lastOutputUpdate := time.Now().UTC().Add(time.Hour * -1)

	for {

		// log.Println("thermostat loop")

		currentTemp, err := ReadTemp_DS18B20(feedbackSensorID)
		if err != nil {
			log.Panicf("thermostat: error reading temp from %s", feedbackSensorID)
			continue
		}

		tempErr := config.Thermostat.Setpoint - currentTemp

		if tempErr > config.Thermostat.Hysteresis && !heaterState {
			//log.Printf("Since the temp err is %.1f, turn on the actuator", tempErr)
			heaterState = true
			err := SetOutputState(config.Thermostat.Actuator, 1)
			if err != nil {
				log.Println(err)
			}
		} else if tempErr < (-1*config.Thermostat.Hysteresis) && heaterState {
			//log.Printf("since the temp err is %.1f, turn off the actuator", tempErr)
			heaterState = false
			err := SetOutputState(config.Thermostat.Actuator, 0)
			if err != nil {
				log.Println(err)
			}
		}

		log.Printf("setpoint %.1f, feedback %.1f, actuator %t", config.Thermostat.Setpoint, currentTemp, heaterState)

		if time.Now().Sub(lastOutputUpdate) > time.Minute*10 {
			lastOutputUpdate = time.Now().UTC()
			outputState := int(0)
			if heaterState {
				outputState = 1
			}
			err := SetOutputState(config.Thermostat.Actuator, outputState)
			if err != nil {
				log.Println(err)
			}
		}

		// log.Printf("sleep for %.1f", runtime.Seconds())
		time.Sleep(runtime)
	}
}

func SetOutputState(outputName string, state int) error {

	log.Printf("outputstate %s -> %d", outputName, state)

	nameMatched := false

	for _, o := range config.DigitalOutputs {

		if o.Name != outputName {
			continue
		}

		pin := rpio.Pin(o.PIN)
		if (state == 0 && !o.InvertedLogic) || (state == 1 && o.InvertedLogic) {
			pin.Write(rpio.Low)

		} else {
			pin.Write(rpio.High)
		}

		time.Sleep(time.Second)
		pinState := pin.Read()
		if pinState == rpio.High {
			log.Printf("output %d high", o.PIN)
		} else {
			log.Printf("output %d low", o.PIN)
		}

		nameMatched = true
	}

	if !nameMatched {
		log.Printf("error outname name '%s' didn't match any actuators", outputName)
	} else {
		var payload string
		if state == 0 {
			payload = "false"
		} else {
			payload = "true"
		}
		mqttClient.Publish(
			fmt.Sprintf("stat/%s/%s", config.MQTT.Topic, outputName),
			0,
			false,
			payload,
		)
	}

	return nil
}

func main() {

	logfileHandle, err := os.OpenFile("fortino.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer logfileHandle.Close()
	wrt := io.MultiWriter(os.Stdout, logfileHandle)
	log.SetOutput(wrt)

	log.Println("starting fortino ...")

	err = rpio.Open()
	if err != nil {
		log.Fatalln(err)
	}
	defer rpio.Close()

	// Interrupt signal callback
	sysSignalChan := make(chan os.Signal, 2)
	signal.Notify(sysSignalChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sysSignalChan // Wait for exit signal
		log.Println("exit signal detected")
		if mqttClient != nil && mqttClient.IsConnected() {

			mqttClient.Disconnect(100)
		}
		os.Exit(1)
	}()

	// Config file reading
	configFile, err := os.Open(configFilePath)
	if err != nil {
		log.Fatalln(err)
	}
	defer configFile.Close()

	byteValue, err := ioutil.ReadAll(configFile)
	if err != nil {
		log.Fatalln(err)
	}

	err = json.Unmarshal(byteValue, &config)
	if err != nil {
		log.Fatalln(err)
	}

	log.Println("digital outputs initialization:")
	// Initialize all outputs to the default values
	for _, v := range config.DigitalOutputs {

		pin := rpio.Pin(v.PIN)
		pin.Output()

		if (v.State && !v.InvertedLogic) || (!v.State && v.InvertedLogic) {
			pin.High()
			log.Printf("- pin %d mode = OUT, state = HIGH", v.PIN)
		} else {
			pin.Low()
			log.Printf("- pin %d mode = OUT, state = LOW", v.PIN)
		}
	}

	mqtt.ERROR = log.New(os.Stdout, "", 0)

	brokerAddr := fmt.Sprintf("tcp://%s:%d", config.MQTT.Host, config.MQTT.Port)

	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerAddr)
	opts.SetClientID("FORTINO_BETA")
	opts.SetUsername(config.MQTT.Username)
	opts.SetPassword(config.MQTT.Password)
	opts.SetKeepAlive(time.Duration(config.MQTT.KeepAlive) * time.Second)
	opts.SetDefaultPublishHandler(mqttCallback)
	opts.SetPingTimeout(30 * time.Second)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(time.Minute)
	opts.SetAutoReconnect(true)
	opts.SetWill(
		fmt.Sprintf("tele/%s/LWT", config.MQTT.Topic),
		"Offline", 0, true,
	)

	mqttClient = mqtt.NewClient(opts)
	token := mqttClient.Connect()
	if token.Wait() && token.Error() != nil {
		log.Fatalln(token.Error())
	}

	cmndTopic := fmt.Sprintf("cmnd/%s/+", config.MQTT.Topic)
	if token := mqttClient.Subscribe(cmndTopic, 0, nil); token.Wait() && token.Error() != nil {
		log.Fatalln(token.Error())
	}
	log.Printf("subscribed to %s", cmndTopic)

	log.Println("sending LWT online message")
	mqttClient.Publish(
		fmt.Sprintf("tele/%s/LWT", config.MQTT.Topic),
		0, true, "Online",
	)

	// Send output states after mqtt connection

	// Sensor update go routine
	if config.UpdateInterval < 3 {
		config.UpdateInterval = 3
	}
	log.Printf("starting sampling loop every %d seconds", config.UpdateInterval)
	go SensorSamplingRuutine(config.UpdateInterval)

	// Thermostat go routine
	if config.Thermostat.Runtime < 10 {
		config.Thermostat.Runtime = 10
	}
	log.Printf("starting thermostat with runtime %d seconds", config.Thermostat.Runtime)
	go ThermostatRoutine(&config.Thermostat)

	time.Sleep(time.Hour * 24 * 5)
}
