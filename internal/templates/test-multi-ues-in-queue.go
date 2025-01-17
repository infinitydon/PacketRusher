/**
 * SPDX-License-Identifier: Apache-2.0
 * © Copyright 2023 Hewlett Packard Enterprise Development LP
 */
package templates

import (
	"fmt"
	"my5G-RANTester/config"
	"my5G-RANTester/internal/common/tools"
	"my5G-RANTester/internal/control_test_engine/procedures"
	"my5G-RANTester/internal/control_test_engine/ue"
	"my5G-RANTester/internal/control_test_engine/ue/context"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestMultiUesInQueue(numUes int, tunnelEnabled bool, dedicatedGnb bool, loop bool, timeBetweenRegistration int, timeBeforeDeregistration int, timeBeforeHandover int, numPduSessions int) {
	if tunnelEnabled && !dedicatedGnb {
		log.Fatal("You cannot use the --tunnel option, without using the --dedicatedGnb option")
	}

	if tunnelEnabled && timeBetweenRegistration < 500 {
		log.Fatal("When using the --tunnel option, --timeBetweenRegistration must be equal to at least 500 ms, or else gtp5g kernel module may crash if you create tunnels too rapidly.")
	}

	if numPduSessions > 16 {
		log.Fatal("You can't have more than 16 PDU Sessions per UE as per spec.")
	}

	wg := sync.WaitGroup{}

	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatal("[TESTER][CONFIG] Unable to read configuration")
	}

	var numGnb int
	if dedicatedGnb {
		numGnb = numUes
	} else {
		numGnb = 1
	}
	if numGnb <= 1 && timeBeforeHandover != 0 {
		log.Warn("[TESTER] We are increasing the number of gNodeB to two for handover test cases. Make you sure you fill the requirements for having two gNodeBs.")
		numGnb++
	}
	gnbs := tools.CreateGnbs(numGnb, cfg, &wg)

	// Wait for gNB to be connected before registering UEs
	// TODO: We should wait for NGSetupResponse instead
	time.Sleep(1 * time.Second)

	msin := cfg.Ue.Msin
	cfg.Ue.TunnelEnabled = tunnelEnabled

	scenarioChans := make([]chan procedures.UeTesterMessage, numUes+1)

	sigStop := make(chan os.Signal, 1)
	signal.Notify(sigStop, os.Interrupt)

	stopSignal := true
	for stopSignal {
		// If CTRL-C signal has been received,
		// stop creating new UEs, else we create numUes UEs
		for ueId := 1; stopSignal && ueId <= numUes; ueId++ {
			ueCfg := cfg
			ueCfg.Ue.Msin = imsiGenerator(ueId, msin)
			log.Info("[TESTER] TESTING REGISTRATION USING IMSI ", ueCfg.Ue.Msin, " UE")

			ueCfg.GNodeB.PlmnList.GnbId = gnbIdGenerator(ueId%numGnb + 1)

			// If there is currently a coroutine handling current UE
			// kill it, before creating a new coroutine with same UE
			// Use case: Registration of N UEs in loop, when loop = true
			if scenarioChans[ueId] != nil {
				scenarioChans[ueId] <- procedures.UeTesterMessage{Type: procedures.Kill}
				close(scenarioChans[ueId])
			}

			// Launch a coroutine to handle UE's individual scenario
			go func(scenarioChan chan procedures.UeTesterMessage, ueId int) {
				wg.Add(1)

				ueRx := make(chan procedures.UeTesterMessage)

				// Create a new UE coroutine
				// ue.NewUE returns context of the new UE
				ueTx := ue.NewUE(ueCfg, uint8(ueId), ueRx, gnbs[ueCfg.GNodeB.PlmnList.GnbId], &wg)

				// We tell the UE to perform a registration
				ueRx <- procedures.UeTesterMessage{Type: procedures.Registration}

				var deregistrationChannel <-chan time.Time = nil
				if timeBeforeDeregistration != 0 {
					deregistrationChannel = time.After(time.Duration(timeBeforeDeregistration) * time.Millisecond)
				}
				var handoverChannel <-chan time.Time = nil
				if timeBeforeHandover != 0 {
					handoverChannel = time.After(time.Duration(timeBeforeHandover) * time.Millisecond)
				}

				loop := true
				state := context.MM5G_NULL
				for loop {
					select {
					case <-deregistrationChannel:
						ueRx <- procedures.UeTesterMessage{Type: procedures.Terminate}
						ueRx = nil
					case <-handoverChannel:
						if ueRx != nil {
							ueRx <- procedures.UeTesterMessage{Type: procedures.Handover, GnbChan: gnbs[gnbIdGenerator((ueId+1)%numGnb+1)].GetInboundChannel()}
						}
					case msg := <-scenarioChan:
						if ueRx != nil {
							ueRx <- msg
						}
					case msg := <-ueTx:
						log.Info("[UE] Switched from state ", state, " to state ", msg.StateChange)
						switch msg.StateChange {
						case context.MM5G_REGISTERED:
							if state != msg.StateChange {
								for i := 0; i < numPduSessions; i++ {
									ueRx <- procedures.UeTesterMessage{Type: procedures.NewPDUSession}
								}
							}
						case context.MM5G_NULL:
							loop = false
						}
						state = msg.StateChange
					}
				}
			}(scenarioChans[ueId], ueId)

			// Before creating a new UE, we wait for timeBetweenRegistration ms
			time.Sleep(time.Duration(timeBetweenRegistration) * time.Millisecond)

			select {
			case <-sigStop:
				stopSignal = false
			default:
			}
		}
		// If loop = false, we don't go over the for loop a second time
		// and we only do the numUes registration once
		if !loop {
			break
		}
	}

	wg.Wait()
}

func imsiGenerator(i int, msin string) string {

	msin_int, err := strconv.Atoi(msin)
	if err != nil {
		log.Fatal("[UE][CONFIG] Given MSIN is invalid")
	}
	base := msin_int + (i - 1)

	imsi := fmt.Sprintf("%010d", base)
	return imsi
}
