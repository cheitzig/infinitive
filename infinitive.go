package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	log "github.com/Sirupsen/logrus"
)

type TStatZoneConfig struct {
	CurrentTemp     uint8  `json:"currentTemp"`
	CurrentHumidity uint8  `json:"currentHumidity"`
	OutdoorTemp     uint8  `json:"outdoorTemp"`
	Mode            string `json:"mode"`
	Stage			uint8  `json:"stage"`
	FanMode         string `json:"fanMode"`
	Hold            *bool  `json:"hold"`
	HeatSetpoint    uint8  `json:"heatSetpoint"`
	CoolSetpoint    uint8  `json:"coolSetpoint"`
	RawMode    		uint8  `json:"rawMode"`
}

type AirHandlerBlower struct {
	BlowerRPM uint16 `json:"blowerRPM"`
}

type AirHandlerDuct struct {
	AirFlowCFM uint16 `json:"airFlowCFM"`
	ElecHeat bool `json:"elecHeat"`
}

type AirHandlerData struct {
	BlowerRPM uint16 `json:"blowerRPM"`
	AirFlowCFM uint16 `json:"airFlowCFM"`
	ElecHeat bool `json:"elecHeat"`
}

type HeatPump struct {
	CoilTemp float32 `json:"coilTemp"`
	OutsideTemp float32 `json:"outsideTemp"`
}

type HeatPumpStage struct {
	Stage uint8 `json:"stage"`
}

type HeatPumpData struct {
	CoilTemp float32 `json:"coilTemp"`
	OutsideTemp float32 `json:"outsideTemp"`
	Stage uint8 `json:"stage"`
}

var infinity *InfinityProtocol

func getConfig() (*TStatZoneConfig, bool) {
	cfg := TStatZoneParams{}
	ok := infinity.Read(devTSTAT, tTSTAT_ZONE_PARAMS, &cfg)
	if !ok {
		return nil, false
	}

	params := TStatCurrentParams{}
	ok = infinity.Read(devTSTAT, tTSTAT_CURRENT_PARAMS, &params)
	if !ok {
		return nil, false
	}

	hold := new(bool)
	*hold = cfg.ZoneHold&0x01 == 1

	return &TStatZoneConfig{
		CurrentTemp:     params.Z1CurrentTemp,
		CurrentHumidity: params.Z1CurrentHumidity,
		OutdoorTemp:     params.OutdoorAirTemp,
		Mode:            rawModeToString(params.Mode & 0xf),
		Stage:      	 params.Mode >> 5,
		FanMode:      rawFanModeToString(cfg.Z1FanMode),
		Hold:         hold,
		HeatSetpoint: cfg.Z1HeatSetpoint,
		CoolSetpoint: cfg.Z1CoolSetpoint,
		RawMode: 		params.Mode,
	}, true
}

func getFanCoil() (*AirHandlerData, bool) {
	b := cache.get("blower")
	tb, ok := b.(*AirHandlerBlower)
	if !ok {
		return nil, false
	}

	d := cache.get("duct")
	td, ok := d.(*AirHandlerDuct)
	if !ok {
		return nil, false
	}
	
	return &AirHandlerData{
		BlowerRPM:     	tb.BlowerRPM,
		AirFlowCFM: 	td.AirFlowCFM,
		ElecHeat: 		td.ElecHeat,
	}, true
}

func getHeatPump() (*HeatPumpData, bool) {
	p := cache.get("heatpump")
	tp, ok := p.(*HeatPump)
	if !ok {
		return nil, false
	}
	
	s := cache.get("heatpumpstage")
	ts, ok := s.(*HeatPumpStage)
	if !ok {
		return nil, false
	}
		
	return &HeatPumpData {
		CoilTemp: tp.CoilTemp,
		OutsideTemp: tp.OutsideTemp,
		Stage: ts.Stage,
	}, true
}

func statePoller() {
	for {
		c, ok := getConfig()
		if ok {
			cache.update("tstat", c)
		}

		time.Sleep(time.Second * 1)
	}
}

func attachSnoops() {
	// Snoop Heat Pump responses
	infinity.snoopResponse(0x5000, 0x51ff, func(frame *InfinityFrame) {
		data := frame.data[3:]

		if bytes.Equal(frame.data[0:3], []byte{0x00, 0x3e, 0x01}) {
			coilTemp := float32(binary.BigEndian.Uint16(data[2:4])) / float32(16)
			outsideTemp := float32(binary.BigEndian.Uint16(data[0:2])) / float32(16)
			cache.update("heatpump", &HeatPump{	CoilTemp: coilTemp,
												OutsideTemp: outsideTemp})

			log.Debugf("heat pump coil temp is: %f", coilTemp)
			log.Debugf("heat pump outside temp is: %f", outsideTemp)
		} else if bytes.Equal(frame.data[0:3], []byte{0x00, 0x3e, 0x02}) {
			stage := data[0] >> 1
			log.Debugf("HP stage is: %d", stage)
			cache.update("heatpumpstage", &HeatPumpStage{ Stage: stage })
		}

	})

	// Snoop Air Handler responses
	infinity.snoopResponse(0x4000, 0x42ff, func(frame *InfinityFrame) {
		data := frame.data[3:]

		if bytes.Equal(frame.data[0:3], []byte{0x00, 0x03, 0x06}) {
			blowerRPM := binary.BigEndian.Uint16(data[1:5])
			log.Debugf("blower RPM is: %d", blowerRPM)
			cache.update("blower", &AirHandlerBlower{BlowerRPM: blowerRPM})
		} else if bytes.Equal(frame.data[0:3], []byte{0x00, 0x03, 0x16}) {
			airFlowCFM := binary.BigEndian.Uint16(data[4:8])
			elecHeat := data[0] & 0x03 != 0
			log.Debugf("air flow CFM is: %d", airFlowCFM)
			cache.update("duct", &AirHandlerDuct{	AirFlowCFM: airFlowCFM,
													ElecHeat: elecHeat })
		}
	})
	
}

func main() {
	httpPort := flag.Int("httpport", 8080, "HTTP port to listen on")
	serialPort := flag.String("serial", "", "path to serial port")

	flag.Parse()

	if len(*serialPort) == 0 {
		fmt.Print("must provide serial\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	log.SetLevel(log.DebugLevel)

	infinity = &InfinityProtocol{device: *serialPort}
	attachSnoops()
	err := infinity.Open()
	if err != nil {
		log.Panicf("error opening serial port: %s", err.Error())
	}

	go statePoller()
	webserver(*httpPort)
}
