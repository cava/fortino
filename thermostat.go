package main

import (
	"fmt"
	"log"
	"time"
)

type Thermostat struct {
	Enabled      bool    `json:"enabled"`
	Setpoint     float64 `json:"setpoint"`
	Actuator     string
	FeedbackType string `json:"feedback_type"`
	FeedbackName string `json:"feedback_name"`
	Regulator    string
	Hysteresis   float64
	Runtime      uint `json:"runtime"`
}

func ThermoSetpoint(setpoint float64) error {
	if setpoint < 5.0 {
		return fmt.Errorf("invalid setpoint %.1f", setpoint)
	} else if setpoint > 25.0 {
		return fmt.Errorf("invalid setpoint %.1f", setpoint)
	}

	config.Thermostat.Setpoint = setpoint
	log.Printf("thermostat: set point = %.1f", config.Thermostat.Setpoint)

	return nil
}

func ThermostatRoutine() {

	if config.Thermostat.Hysteresis < 0 {
		log.Fatal("thermostat: hysteresis can not be negative")
	}

	var feedbackSensorID string
	for _, s := range config.Onewires {
		if s.Name == config.Thermostat.FeedbackName {
			feedbackSensorID = s.ID
		}
	}
	if len(feedbackSensorID) == 0 {
		log.Printf("thermostat: invalid feedback %s", config.Thermostat.FeedbackName)
		return
	}

	heaterState := false

	time.Sleep(time.Second * 10)
	runtime := (time.Second * time.Duration(config.Thermostat.Runtime))

	lastOutputUpdate := time.Now().UTC().Add(time.Hour * -1)

	for {
		currentTemp, err := ReadTemp_DS18B20(feedbackSensorID)
		if err != nil {
			log.Panicf("thermostat: error reading temp from %s", feedbackSensorID)
			continue
		}

		tempErr := (config.Thermostat.Setpoint - currentTemp)

		if tempErr > config.Thermostat.Hysteresis && !heaterState {
			log.Printf("thermostat: Since the temp err is %.1f, turn on the actuator", tempErr)
			heaterState = true
			err := SetOutputState(config.Thermostat.Actuator, 1)
			if err != nil {
				log.Println(err)
			}
		} else if tempErr < (-1*config.Thermostat.Hysteresis) && heaterState {
			log.Printf("thermostat: since the temp err is %.1f, turn off the actuator", tempErr)
			heaterState = false
			err := SetOutputState(config.Thermostat.Actuator, 0)
			if err != nil {
				log.Println(err)
			}
		}

		// log.Printf("setpoint %.1f, feedback %.1f, actuator %t", config.Thermostat.Setpoint, currentTemp, heaterState)

		if time.Now().UTC().Sub(lastOutputUpdate) > time.Minute*10 {
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

		time.Sleep(runtime)
	}
}
