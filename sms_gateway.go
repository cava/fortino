package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// stores the HiLink connection state
type HiLink struct {
	Address    string
	Token      string
	SessionID  string
	LastReadID int
}

type HiLinkConfig struct {
	Enable        bool     `json:"enabled"`
	Address       string   `json:"address"`
	AllowedPhones []string `json:"allowed_phones"`
}

type HiLinkMessagesResp struct {
	Count    int `xml:"Count"`
	Messages struct {
		MessageList []HiLinkMsg `xml:"Message"`
	} `xml:"Messages"`
}

type HiLinkMsg struct {
	Smstat   int `xml:"Smstat"`
	Index    int `xml:"Index"`
	Phone    string
	Content  string
	Date     string
	Sca      string
	SaveType int
	Priority int
	SmsType  int
}

const HELP_MSG = "puoi inviare:\naiuto\ntemp"

// Regex that starts with '(?i)' is case insentitive
var thermostatSetTempRegex = regexp.MustCompile(`(?i)term ([0-9]{1,2})`)

func (h *HiLink) FetchSession() error {

	log.Println("sms: retriving session ID ...")

	resp, err := http.Get(
		fmt.Sprintf("http://%s/html/index.html", h.Address),
	)
	if err != nil {
		log.Println(err)
		return err
	}
	defer resp.Body.Close()

	for _, c := range resp.Cookies() {
		if c.Name == "SessionID" {
			h.SessionID = c.Value
		}
	}

	if len(h.SessionID) == 0 {
		log.Println("sms: Unable to get SessionID!")
	} else {
		log.Printf("sms: SessionID = %s\n", h.SessionID)
	}

	return nil
}

func (h *HiLink) GetToken() error {
	httpClient := &http.Client{}

	tokenReq, err := http.NewRequest(
		"GET",
		fmt.Sprintf("http://%s/html/smsinbox.html", h.Address),
		nil,
	)
	if err != nil {
		return err
	}
	tokenReq.Header.Set("Cookie", fmt.Sprintf("SessionID=%s", h.SessionID))

	res, err := httpClient.Do(tokenReq)
	if err != nil {
		return err
	}

	defer res.Body.Close()

	bodyyy, _ := ioutil.ReadAll(res.Body)

	splits := strings.SplitN(string(bodyyy), "\"", 11)

	if splits[3] == "csrf_token" {
		h.Token = splits[5]
	} else if splits[7] == "csrf_token" {
		h.Token = splits[9]
	} else {
		h.Token = ""
	}

	log.Printf("sms: received token = %s", h.Token)
	return nil
}

func (h *HiLink) SendMessage(number string, content string) error {

	log.Printf("sms: sending %s to %s", content, number)

	now := time.Now().Format("2006-01-02 15:04:05")

	postData := "<request><Index>-1</Index><Phones><Phone>%s</Phone></Phones><Sca/><Content>%s</Content><Length>%d</Length><Reserved>1</Reserved><Date>%s</Date></request>"
	postD := fmt.Sprintf(postData, number, content, len(content), now)

	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf("http://%s/api/sms/send-sms", h.Address),
		bytes.NewBuffer([]byte(postD)),
	)
	if err != nil {
		log.Println(err)
		return err
	}

	req.Header["__RequestVerificationToken"] = []string{h.Token}
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Cookie", fmt.Sprintf("SessionID=%s", h.SessionID))
	//fmt.Printf("%+v\n", req.Header)

	client := &http.Client{}
	respp, err := client.Do(req)
	if err != nil {
		log.Println(err)
		return err
	}
	defer respp.Body.Close()

	//fmt.Println("response Status:", respp.Status)
	//fmt.Println("response Headers:", respp.Header)
	bodyyy, _ := ioutil.ReadAll(respp.Body)
	//fmt.Println("response Body:", string(bodyyy))

	if strings.Contains(string(bodyyy), "<response>OK</response>") {
		log.Printf("sms: Message sent\n")
	} else {
		log.Printf("sms: received the following response: %s\n", bodyyy)
	}

	return nil
}

func (h *HiLink) GetMessages(len int) (*HiLinkMessagesResp, error) {

	postData := "<request><PageIndex>1</PageIndex><ReadCount>10</ReadCount><BoxType>1</BoxType><SortType>0</SortType><Ascending>0</Ascending><UnreadPreferred>0</UnreadPreferred></request>"
	//postD := fmt.Sprintf(postData, len)
	postD := postData

	req, err := http.NewRequest(
		"POST",
		fmt.Sprintf("http://%s/api/sms/sms-list", h.Address),
		bytes.NewBuffer([]byte(postD)),
	)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	req.Header["__RequestVerificationToken"] = []string{h.Token}
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Cookie", fmt.Sprintf("SessionID=%s", h.SessionID))

	client := &http.Client{}
	respp, err := client.Do(req)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	defer respp.Body.Close()

	bodyyy, err := ioutil.ReadAll(respp.Body)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	var messages HiLinkMessagesResp
	err = xml.Unmarshal(bodyyy, &messages)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	return &messages, nil
}

func (h *HiLink) InitializeLastReadMsg() error {
	msgs, err := h.GetMessages(10)
	if err != nil {
		return err
	}

	if len(msgs.Messages.MessageList) == 0 {
		return errors.New("sms: returned message list is empty")
	}

	if msgs.Messages.MessageList[0].Index == 0 {
		return errors.New("sms: last message index is 0, could be a parsing error!")
	}

	// da verificare che non siano arrivati messaggi di recente

	h.LastReadID = msgs.Messages.MessageList[0].Index

	return nil
}

func parseHiLinkDate(dateStr string) (time.Time, error) {
	// es. 2021-12-28 21:53:14
	layout := "2006-01-02 15:04:05"
	return time.Parse(layout, dateStr)
}

func HandleNewMessage(hiLink *HiLink, msg HiLinkMsg) {
	log.Printf("sms: received '%s' from %s\n", msg.Content, msg.Phone)

	IsAllowed := false
	for _, p := range config.HiLinkConfig.AllowedPhones {
		if p == msg.Phone {
			IsAllowed = true
			break
		}
	}

	if !IsAllowed {
		log.Printf("sms: phone %s isn't allowed to send commands, ignoring\n", msg.Phone)
		return
	}

	err := hiLink.FetchSession()
	if err != nil {
		log.Println("in HandleNewMessage -> FetchSession")
		log.Println(err)
		return
	}
	err = hiLink.GetToken()
	if err != nil {
		log.Println("in HandleNewMessage -> GetRoken")
		log.Println(err)
		return
	}

	if strings.ToLower(msg.Content) == "aiuto" ||
		strings.ToLower(msg.Content) == "help" {
		hiLink.SendMessage(msg.Phone, HELP_MSG)
	} else if strings.ToLower(msg.Content) == "temp" {

		body := ""

		for _, t := range config.Onewires {
			temp, err := ReadTemp_DS18B20(t.ID)
			if err == nil {
				body = body + fmt.Sprintf("%s: %2.1f\n", t.ID, temp)
			}
		}
		hiLink.SendMessage(msg.Phone, body)
	} else if strings.ToLower(msg.Content) == "term" {
		body := fmt.Sprintf("t_setpoint = %2.1f", config.Thermostat.Setpoint)
		hiLink.SendMessage(msg.Phone, body)
	} else if thermostatSetTempRegex.Match([]byte(msg.Content)) {
		// Since it matched
		matches := thermostatSetTempRegex.FindStringSubmatch(msg.Content)
		if len(matches) < 2 {
			log.Fatal("sms term setpoint matching: there should be at least one match")
		}
		termSetPoint, err := strconv.ParseUint(string(matches[1]), 10, 16)
		if err != nil {
			log.Println("error: unable to parse an uint after regex")
			return
		}

		err = ThermoSetpoint(float64(termSetPoint))
		if err != nil {
			log.Println(err)
			hiLink.SendMessage(msg.Phone, "invalid command")
			return
		}

		log.Printf("sms: %s changed thermostat set point to %d C\n", msg.Phone, termSetPoint)
		hiLink.SendMessage(msg.Phone, fmt.Sprintf("Ok, temp = %d C", termSetPoint))
	}
}

func HiLinkRoutine(address string) {
	hiLink := &HiLink{}
	hiLink.Address = address

	i := uint32(0)
	for {
		if i > 0 {
			time.Sleep(time.Minute * 30)
		} else {
			time.Sleep(time.Second * 15)
		}
		i = i + 1

		if len(hiLink.SessionID) == 0 {
			log.Printf("sms: invalid HiLink SessionID [%d]\n", i)
			err := hiLink.FetchSession()
			if err != nil {
				hiLink.SessionID = ""
				hiLink.Token = ""
			}
		}

		if len(hiLink.Token) == 0 {
			log.Printf("sms: invalid HiLink Token [%d]\n", i)
			err := hiLink.GetToken()
			if err != nil {
				hiLink.SessionID = ""
				hiLink.Token = ""
			}
		}

		log.Println("sms: reading messages")

		msgs, err := hiLink.GetMessages(2)
		if err != nil {
			log.Println("sms: error from GetMessages()")
			log.Println(err)
			hiLink.SessionID = ""
			hiLink.Token = ""
			// i = 0
			continue
		}

		LatestReadID := 0

		for _, m := range msgs.Messages.MessageList {
			// log.Println(m)

			messageDate, err := parseHiLinkDate(m.Date)
			if err != nil {
				log.Printf("sms: unable to parse date %s\n", m.Date)
				break
			}
			timeDelta := time.Since(messageDate)
			if timeDelta.Minutes() > 60 {
				break
			}

			if m.Index > hiLink.LastReadID {
				HandleNewMessage(hiLink, m)
			}
			if m.Index > LatestReadID {
				LatestReadID = m.Index
			}
			break
		}

		if LatestReadID > 0 && LatestReadID > hiLink.LastReadID {
			hiLink.LastReadID = LatestReadID
		} else {
			hiLink.Token = ""
			hiLink.SessionID = ""
			i = 0
		}

	}
}
