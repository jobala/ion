package plugins

import (
	"errors"
	"fmt"
	"time"

	"github.com/pion/ion/pkg/log"
	"github.com/pion/ion/pkg/util"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

const (
	bufferSize = 200
	rembLowBW  = 30 * 1000
	rembHighBW = 500 * 1000
)

type JitterBuffer struct {
	id       string
	buffers  map[uint32]*Buffer
	rtcpCh   chan rtcp.Packet
	stop     bool
	byteRate uint64
	lostRate float64

	rembCycle int
	pliCycle  int
}

func NewJitterBuffer(id string) *JitterBuffer {
	j := &JitterBuffer{
		buffers: make(map[uint32]*Buffer),
		id:      id,
		rtcpCh:  make(chan rtcp.Packet, 100),
	}
	j.rembLoop()
	j.pliLoop()
	return j
}

// Init args: (ssrc uint32, pt uint8, rembCycle int, pliCycle int)
func (j *JitterBuffer) Init(args ...interface{}) {
	ssrc := args[0].(uint32)
	pt := args[1].(uint8)
	j.rembCycle = args[2].(int)
	j.pliCycle = args[3].(int)

	if j.rembCycle > 10 {
		j.rembCycle = 10
	}

	if j.pliCycle > 10 {
		j.pliCycle = 10
	}

	if j.GetBuffer(ssrc) == nil {
		log.Infof("JitterBuffer.Init j.AddBuffer %d", ssrc)
		j.AddBuffer(ssrc).SetSSRCPT(ssrc, pt)
	}

	log.Infof("JitterBuffer.Init pli=%d remb=%d", j.pliCycle, j.rembCycle)
}

func (j *JitterBuffer) ID() string {
	return j.id
}

func (j *JitterBuffer) GetRTCPChan() chan rtcp.Packet {
	return j.rtcpCh
}

func (j *JitterBuffer) AddBuffer(ssrc uint32) *Buffer {
	p := NewBuffer(bufferSize)
	j.buffers[ssrc] = p
	j.nackLoop(p)
	return p
}

func (j *JitterBuffer) GetBuffer(ssrc uint32) *Buffer {
	return j.buffers[ssrc]
}

func (j *JitterBuffer) GetBuffers() map[uint32]*Buffer {
	return j.buffers
}

// PushRTP push rtp packet which from pub
func (j *JitterBuffer) PushRTP(pkt *rtp.Packet) error {
	ssrc := pkt.SSRC

	p := j.GetBuffer(ssrc)
	if p == nil {
		p = j.AddBuffer(ssrc)
	}
	if p == nil {
		return errors.New("buffer is nil")
	}

	p.Push(pkt)
	return nil
}

// PushRTCP push rtcp packet which from sub
func (j *JitterBuffer) PushRTCP(pkt rtcp.Packet) error {
	// log.Infof("JitterBuffer.PushRTCP %v", pkt)
	return nil
}
func (j *JitterBuffer) nackLoop(p *Buffer) {
	go func() {
		for nack := range p.GetRTCPChan() {
			if j.stop {
				return
			}
			j.rtcpCh <- nack
		}
	}()
}

func (j *JitterBuffer) rembLoop() {
	go func() {
		for {
			if j.stop {
				return
			}

			if j.rembCycle <= 0 {
				time.Sleep(time.Second)
				continue
			}

			time.Sleep(time.Duration(j.rembCycle) * time.Second)
			for _, buffer := range j.GetBuffers() {
				j.lostRate, j.byteRate = buffer.CalcLostRateByteRate(1)
				var bw uint64
				if j.lostRate == 0 && j.byteRate == 0 {
					bw = rembHighBW
				} else if j.lostRate >= 0 && j.lostRate < 0.1 {
					bw = j.byteRate * 2
				} else {
					bw = uint64(float64(j.byteRate) * (1 - j.lostRate))
				}

				if bw < rembLowBW {
					bw = rembLowBW
				}

				if bw > rembHighBW {
					bw = rembHighBW
				}

				remb := &rtcp.ReceiverEstimatedMaximumBitrate{
					SenderSSRC: buffer.GetSSRC(),
					Bitrate:    bw * 8,
					SSRCs:      []uint32{buffer.GetSSRC()},
				}
				j.rtcpCh <- remb
			}
		}
	}()
}

func (j *JitterBuffer) pliLoop() {
	go func() {
		for {
			if j.stop {
				return
			}

			if j.pliCycle <= 0 {
				time.Sleep(time.Second)
				continue
			}
			time.Sleep(time.Duration(j.pliCycle) * time.Second)
			for _, buffer := range j.GetBuffers() {
				if util.IsVideo(buffer.GetPayloadType()) {
					pli := &rtcp.PictureLossIndication{SenderSSRC: buffer.GetSSRC(), MediaSSRC: buffer.GetSSRC()}
					j.rtcpCh <- pli
				}
			}
		}
	}()
}

func (j *JitterBuffer) GetPacket(ssrc uint32, sn uint16) *rtp.Packet {
	buffer := j.buffers[ssrc]
	if buffer == nil {
		return nil
	}
	return buffer.GetPacket(sn)
}

func (j *JitterBuffer) Stop() {
	if j.stop {
		return
	}
	j.stop = true
	for _, buffer := range j.buffers {
		buffer.Stop()
	}
}

func (j *JitterBuffer) Stat() string {
	out := ""
	for ssrc, buffer := range j.buffers {
		out += fmt.Sprintf("ssrc:%d payload:%d | lostRate:%.2f | byteRate:%d | %s", ssrc, buffer.GetPayloadType(), j.lostRate, j.byteRate, buffer.GetStat())
	}
	return out
}
