// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

// read-rtx is a simple application that shows how to record your webcam/microphone using Pion WebRTC and read rtx
package main

import (
	"fmt"
	"github.com/pion/rtcp"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/examples/internal/signal"
)

const (
	nackInterval = time.Second
	lostPacket   = 0
)

// readRTPAndSendNack read rtp and send rtcp nack sometimes
func readRTPAndSendNack(pc *webrtc.PeerConnection, track *webrtc.TrackRemote) {
	nackTime := time.Now()
	for {
		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			if err == io.EOF {
				panic(err)
			}
			continue
		}
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			// Assume that packet is lost and nack is sent
			if checkNackInterval(&nackTime) {
				nack := makeNack(rtpPacket.SSRC, rtpPacket.SequenceNumber)
				if err := pc.WriteRTCP(nack); err != nil {
					panic(err)
				}
				fmt.Printf("Send Nack sequence:%d\n", rtpPacket.SequenceNumber)
				continue
			}
		}

		// you should use jitter
	}
}

func readRTX(track *webrtc.TrackRemote) {
	if !track.HasRTX() {
		return
	}
	for {
		osn, rtxPacket, _, err := track.ReadRTX()
		if err != nil {
			if err == io.EOF {
				return
			}
			continue
		}

		// some stats if you want

		if len(rtxPacket.Payload) == 0 {
			// padding probes
			fmt.Println("Got RTX padding packets. rtx sn:", rtxPacket.SequenceNumber)
			continue
		}

		fmt.Println("Got RTX Packet. osn:", osn, ", rtx sn:", rtxPacket.SequenceNumber)

		// you should use jitter
	}
}

func checkNackInterval(lastNackTime *time.Time) bool {
	if time.Since(*lastNackTime) <= nackInterval {
		return false
	}

	*lastNackTime = time.Now()
	return true
}

func makeNack(ssrc uint32, sequenceNumber uint16) []rtcp.Packet {
	return []rtcp.Packet{&rtcp.TransportLayerNack{
		MediaSSRC: ssrc,
		Nacks: []rtcp.NackPair{{
			PacketID:    sequenceNumber,
			LostPackets: rtcp.PacketBitmap(lostPacket),
		}},
	}}
}

// nolint:gocognit
func main() {
	// Everything below is the Pion WebRTC API! Thanks for using it ❤️.

	// Create a MediaEngine object to configure the supported codec
	m := &webrtc.MediaEngine{}

	// Setup the codecs you want to use.
	// We'll use a VP8, video/rtx and Opus but you can also define your own
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, Channels: 0, SDPFmtpLine: "apt=96", RTCPFeedback: nil},
		PayloadType:        97,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	// Create a InterceptorRegistry. This is the user configurable RTP/RTCP Pipeline.
	// This provides NACKs, RTCP Reports and other features. If you use `webrtc.NewPeerConnection`
	// this is enabled by default. If you are manually managing You MUST create a InterceptorRegistry
	// for each PeerConnection.
	i := &interceptor.Registry{}

	// Register a intervalpli factory
	// This interceptor sends a PLI every 3 seconds. A PLI causes a video keyframe to be generated by the sender.
	// This makes our video seekable and more error resilent, but at a cost of lower picture quality and higher bitrates
	// A real world application should process incoming RTCP packets from viewers and forward them to senders
	intervalPliFactory, err := intervalpli.NewReceiverInterceptor()
	if err != nil {
		panic(err)
	}
	i.Add(intervalPliFactory)

	// Use the default set of Interceptors
	if err = webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		panic(err)
	}

	// Use Disable SRTP Replay Protection. this is option For only RTX Test. It is usually false
	settingEngine := webrtc.SettingEngine{}
	settingEngine.DisableSRTPReplayProtection(true)
	// Create the API object with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i), webrtc.WithSettingEngine(settingEngine))

	// Prepare the configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	// Allow us to receive 1 audio track, and 1 video track
	if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	} else if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}

	// Set a handler for when a new remote track starts
	// this handler call readRTPAndSendNack and readRTX functions.
	// readRTPAndSendNack read packets from rtp stream and send rtcp nack sometimes
	// readRTX read packets from rtx stream and print rtx information
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		codec := track.Codec()
		if strings.EqualFold(codec.MimeType, webrtc.MimeTypeOpus) {
			fmt.Println("Got Audio track hasRTX:", track.HasRTX())
		} else if strings.EqualFold(codec.MimeType, webrtc.MimeTypeVP8) {
			fmt.Println("Got Video track hasRTX:", track.HasRTX())
		}
		go readRTPAndSendNack(peerConnection, track)
		go readRTX(track)
	})

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())

		if connectionState == webrtc.ICEConnectionStateConnected {
			fmt.Println("Ctrl+C the remote client to stop the demo")
		} else if connectionState == webrtc.ICEConnectionStateFailed {
			fmt.Println("End demo")

			// Gracefully shutdown the peer connection
			if closeErr := peerConnection.Close(); closeErr != nil {
				panic(closeErr)
			}

			os.Exit(0)
		}
	})

	// Wait for the offer to be pasted
	offer := webrtc.SessionDescription{}
	signal.Decode(signal.MustReadStdin(), &offer)

	// Set the remote SessionDescription
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		panic(err)
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and starts our UDP listeners
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		panic(err)
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	// Output the answer in base64 so we can paste it in browser
	fmt.Println(signal.Encode(*peerConnection.LocalDescription()))

	// Block forever
	select {}
}
