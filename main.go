package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v2"
)

// Reference
// https://github.com/pion/webrtc/blob/5f54b688995a424fbe5764a4e753a7deea62d04e/examples/broadcast/main.go
// https://github.com/pion/webrtc/blob/5f54b688995a424fbe5764a4e753a7deea62d04e/examples/play-from-disk/main.go

const rtcpPLIInterval = time.Second * 3
const indexHTML = `
<html>
<head>
<title>WebRTC-RTP Forwarder Sample - Video Sender</title>
<script>
'use strict';
let peer = null;
let conn = null;
let local_stream = null;
function startConnect() {
	conn = new WebSocket('ws://localhost:8080/ws');
	conn.onmessage = (evt) => {
		let d =JSON.parse(evt.data);
		let type = d['type'];
		let payload = d['payload'];

		if (type === 'answer') {
			peer.setRemoteDescription(new RTCSessionDescription({
				type: 'answer',
				sdp: payload,
			})).then((o) => {
				console.log(o);
			}).catch((e) => {
				console.err(e);
			});
		} else {
			console.error('unexpected message', d);
		}
	};
	conn.onopen = (evt) => {
		let config = null;

		peer = new RTCPeerConnection(config);
		peer.ontrack = (evt) => {
			console.log(evt);
			remote_video.srcObject = evt.streams[0];
		};
		peer.onicecandidate = (evt) => {
			console.log(evt);
			if (!evt.candidate) {
				return;
			}
			conn.send(JSON.stringify({
				type: 'candidate',
				payload: evt.candidate.candidate
			}));
		};
		local_stream.getTracks().forEach(track => peer.addTrack(track, local_stream));
		peer.createOffer().then((offer) => {
			return peer.setLocalDescription(offer)
		}).then(() => {
			conn.send(JSON.stringify({
				type: 'offer',
				payload: peer.localDescription.sdp
			}));
		}).catch((e) => {
			console.error(e);
		});
	};
	conn.onclose = (evt) => {
		console.log('Closed connection.');
		conn = null;
	};
}
window.onload = () => {
	navigator.mediaDevices.getUserMedia({
		video: true,
		audio: true,
	}).then((stream) => {
		local_video.srcObject = stream;
		local_video.volume = 0;
		local_stream = stream;
		startConnect();
	}).catch(err => {
		console.log(JSON.stringify(err));
	});
};
</script>
</head>
<body>
	<video id="local_video" autoplay></video><br>
	<video id="remote_video" autoplay controls></video>
</body>
</html>
`

type message struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

func messageReceiver(conn *websocket.Conn, msgch chan message) {
	m := message{}
	for {
		if err := websocket.ReadJSON(conn, &m); err != nil {
			fmt.Fprintf(os.Stderr, "websocket.ReadJSON() returns ClosedError %v\n", err)
			close(msgch)
			return
		} else {
			fmt.Printf("websocket.ReadJSON() returns %v\n", m)
			msgch <- m
		}
	}
}

func main() {
	m := webrtc.MediaEngine{}
	m.RegisterCodec(webrtc.NewRTPVP8Codec(webrtc.DefaultPayloadTypeVP8, 90000))
	m.RegisterCodec(webrtc.NewRTPOpusCodec(webrtc.DefaultPayloadTypeOpus, 48000))

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m))

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, indexHTML)
	})
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "upgrader.Upgrade() failed.\n")
			return
		}
		defer conn.Close()

		msgch := make(chan message)
		go messageReceiver(conn, msgch)

		peerConnection, err := api.NewPeerConnection(webrtc.Configuration{
			ICEServers: []webrtc.ICEServer{
				{
					URLs: []string{"stun:stun.l.google.com:19302"},
				},
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "api.NewPeerConnection() failed. %v\n", err)
			return
		}

		var localVideoTrack *webrtc.Track = nil
		var localAudioTrack *webrtc.Track = nil

		peerConnection.OnTrack(func(remoteTrack *webrtc.Track, receiver *webrtc.RTPReceiver) {
			fmt.Printf("peerConnection.OnTrack(%v)\n", remoteTrack)

			go func() {
				ticker := time.NewTicker(rtcpPLIInterval)
				fmt.Printf("On rtcpPLIInterval.\n")
				for range ticker.C {
					fmt.Printf("On rtcpPLIInterval.\n")
					if rtcpSendErr := peerConnection.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: remoteTrack.SSRC()}}); rtcpSendErr != nil {
						fmt.Println(rtcpSendErr)
						return
					}
				}
			}()

			for {
				rtpPacket, err := remoteTrack.ReadRTP()
				if err != nil {
					return
				}

				if localVideoTrack == nil {
					fmt.Printf("remoteTrack.Read() continue.\n")
					continue
				}

				switch rtpPacket.PayloadType {
				case localVideoTrack.PayloadType():
					rtpPacket.SSRC = localVideoTrack.SSRC()
				case localAudioTrack.PayloadType():
					rtpPacket.SSRC = localAudioTrack.SSRC()
				default:
					continue
				}

				err = localVideoTrack.WriteRTP(rtpPacket)
				if err != nil {
					fmt.Fprintf(os.Stderr, "localVideoTrack.Write() failed. %v\n", err)
					return
				}
			}
		})

		localVideoTrack, err = peerConnection.NewTrack(webrtc.DefaultPayloadTypeVP8, rand.Uint32(), "video", "pion")
		if err != nil {
			fmt.Fprintf(os.Stderr, "peerConnection.NewTrack(VP8) failed. %v\n", err)
			return
		}

		_, err = peerConnection.AddTrack(localVideoTrack)
		if err != nil {
			fmt.Fprintf(os.Stderr, "peerConnection.AddTrack(Video) failed. %v\n", err)
			return
		}

		localAudioTrack, err = peerConnection.NewTrack(webrtc.DefaultPayloadTypeOpus, rand.Uint32(), "audio", "pion")
		if err != nil {
			fmt.Fprintf(os.Stderr, "peerConnection.NewTrack(OPUS) failed. %v\n", err)
			return
		}

		_, err = peerConnection.AddTrack(localAudioTrack)
		if err != nil {
			fmt.Fprintf(os.Stderr, "peerConnection.AddTrack(Audio) failed. %v\n", err)
			return
		}

		for {
			select {
			case m, ok := <-msgch:
				if ok {
					switch m.Type {
					case "offer":
						{
							desc := webrtc.SessionDescription{
								Type: webrtc.SDPTypeOffer,
								SDP:  m.Payload,
							}
							err = peerConnection.SetRemoteDescription(desc)
							if err != nil {
								fmt.Fprintf(os.Stderr, "peerConnection.SetRemoteDescription() failed. %v\n", err)
								goto close
							}

							answer, err := peerConnection.CreateAnswer(nil)
							if err != nil {
								fmt.Fprintf(os.Stderr, "peerConnection.CreateAnswer() failed. %v\n", err)
								goto close
							}

							err = peerConnection.SetLocalDescription(answer)
							if err != nil {
								fmt.Fprintf(os.Stderr, "peerConnection.CreateAnswer() failed. %v\n", err)
								goto close
							}

							websocket.WriteJSON(conn, &message{
								Type:    "answer",
								Payload: answer.SDP,
							})
						}
					case "candidate":
						{
							candidate := webrtc.ICECandidateInit{
								Candidate: m.Payload,
							}
							err := peerConnection.AddICECandidate(candidate)
							if err != nil {
								fmt.Fprintf(os.Stderr, "peerConnection.AddICECandidate() failed. %v\n", err)
							}
						}
					}
				} else {
					goto close
				}
			}
		}
	close:
	})
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Fprintf(os.Stderr, "httpListenAndServe() failed: %v\n", err)
	}
}
