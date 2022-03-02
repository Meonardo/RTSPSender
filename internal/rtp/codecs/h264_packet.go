package codecs

import (
	"encoding/binary"
	"fmt"
	"math"
)

// H264Payloader payloads H264 packets
type H264Payloader struct {
	spsNalu, ppsNalu []byte
}

const (
	stapaNALUType  = 24
	fuaNALUType    = 28
	fubNALUType    = 29
	spsNALUType    = 7
	ppsNALUType    = 8
	audNALUType    = 9
	fillerNALUType = 12

	nalHeaderSize 		= 1
	fuaHeaderSize       = 2
	stapaHeaderSize     = 1
	stapaNALULengthSize = 2

	naluTypeBitmask   = 0x1F
	naluRefIdcBitmask = 0x60
	fuStartBitmask    = 0x80
	fuEndBitmask      = 0x40

	outputStapAHeader = 0x78
)

func annexbNALUStartCode() []byte { return []byte{0x00, 0x00, 0x00, 0x01} }

func packetizeFuA(mtu int, payload []byte) [][]byte {
	availableSize := mtu - fuaHeaderSize
	payloadSize := len(payload) - nalHeaderSize
	numPackets := int(math.Ceil(float64(payloadSize) / float64(availableSize)))
	numLargerPackets := payloadSize % numPackets
	packageSize := int(math.Floor(float64(payloadSize) / float64(numPackets)))

	fNRI := payload[0] & (0x80 | 0x60)
	nal := payload[0] & naluTypeBitmask

	fuIndicator := fNRI | fuaNALUType

	fuHeaderEnd := []byte{fuIndicator, nal | fuEndBitmask}
	fuHeaderMiddle := []byte{fuIndicator, nal}
	fuHeaderStart := []byte{fuIndicator, nal | fuStartBitmask}
	fuHeader := fuHeaderStart

	var packages [][]byte
	var offset = nalHeaderSize

	for offset < len(payload) {
		var data []byte
		if numLargerPackets > 0 {
			numLargerPackets -= 1
			data = payload[offset : (offset + packageSize + 1)]
			offset += packageSize + 1
		} else {
			data = payload[offset : (offset + packageSize)]
			offset += packageSize
		}

		if offset == len(payload) {
			fuHeader = fuHeaderEnd
		}

		t := append(fuHeader, data...)
		packages = append(packages, t)

		fuHeader = fuHeaderMiddle
	}

	return packages
}

func emitNalus(nals []byte, emit func([]byte)) {
	// query start code
	nextInd := func(nalu []byte, start int) (indStart int, indLen int) {
		zeroCount := 0

		for i, b := range nalu[start:] {
			if b == 0 {
				zeroCount++
				continue
			} else if b == 1 {
				if zeroCount >= 2 {
					return start + i - zeroCount, zeroCount + 1
				}
			}
			zeroCount = 0
		}
		return -1, -1
	}

	nextIndStart, nextIndLen := nextInd(nals, 0)
	if nextIndStart == -1 {
		emit(nals)
	} else {
		for nextIndStart != -1 {
			prevStart := nextIndStart + nextIndLen
			nextIndStart, nextIndLen = nextInd(nals, prevStart)
			if nextIndStart != -1 {
				emit(nals[prevStart:nextIndStart])
			} else {
				// Emit until end of stream, no end indicator found
				emit(nals[prevStart:])
			}
		}
	}
}

// Payload fragments a H264 packet across one or more byte arrays
func (p *H264Payloader) Payload(mtu uint16, payload []byte) [][]byte {
	max := int(mtu) + 12

	var payloads [][]byte
	if len(payload) == 0 {
		return payloads
	}
	//println("payload len: ", len(payload))

	counter := 0
	availableSize := max - (nalHeaderSize + stapaNALULengthSize)
	var stapAPayload []byte
	var stapHeader byte
	var shouldAppendSTAPA = false

	emitNalus(payload, func(nalu []byte) {
		if len(nalu) == 0 {
			return
		}

		if len(nalu) > max {
			if shouldAppendSTAPA {
				if counter <= 1 {
					payloads = append(payloads, nalu)
				} else {
					stapAPayload = append([]byte{stapHeader}, stapAPayload...)
					payloads = append(payloads, stapAPayload)
				}
			}

			shouldAppendSTAPA = false

			fuaPacket := packetizeFuA(max, nalu)
			payloads = append(payloads, fuaPacket...)
		} else {
			if stapHeader == 0 {
				stapHeader = stapaNALUType | (nalu[0] & 0xE0)
			}

			if len(nalu) <= availableSize && counter < 9 {
				stapHeader |= nalu[0] & 0x80
				nri := nalu[0] & 0x60

				if stapHeader & 0x60 < nri {
					stapHeader = stapHeader & 0x9F | nri
				}
				availableSize -= stapaNALULengthSize + len(nalu)
				counter += 1

				naluWithLen := make([]byte, 2)
				binary.BigEndian.PutUint16(naluWithLen, uint16(len(nalu)))
				naluWithLen = append(naluWithLen, nalu...)

				stapAPayload = append(naluWithLen, stapAPayload...)

				shouldAppendSTAPA = true
				return
			}
		}
	})

	//for i, t := range payloads {
	//	fmt.Printf("Packet index: %d len: %d\n", i, len(t))
	//}
	return payloads
}

// H264Packet represents the H264 header that is stored in the payload of an RTP Packet
type H264Packet struct {
	IsAVC     bool
	fuaBuffer []byte

	videoDepacketizer
}

func (p *H264Packet) doPackaging(nalu []byte) []byte {
	if p.IsAVC {
		naluLength := make([]byte, 4)
		binary.BigEndian.PutUint32(naluLength, uint32(len(nalu)))
		return append(naluLength, nalu...)
	}

	return append(annexbNALUStartCode(), nalu...)
}

// IsDetectedFinalPacketInSequence returns true of the packet passed in has the
// marker bit set indicated the end of a packet sequence
func (p *H264Packet) IsDetectedFinalPacketInSequence(rtpPacketMarketBit bool) bool {
	return rtpPacketMarketBit
}

// Unmarshal parses the passed byte slice and stores the result in the H264Packet this method is called upon
func (p *H264Packet) Unmarshal(payload []byte) ([]byte, error) {
	if payload == nil {
		return nil, errNilPacket
	} else if len(payload) <= 2 {
		return nil, fmt.Errorf("%w: %d <= 2", errShortPacket, len(payload))
	}

	// NALU Types
	// https://tools.ietf.org/html/rfc6184#section-5.4
	naluType := payload[0] & naluTypeBitmask
	switch {
	case naluType > 0 && naluType < 24:
		return p.doPackaging(payload), nil

	case naluType == stapaNALUType:
		currOffset := int(stapaHeaderSize)
		result := []byte{}
		for currOffset < len(payload) {
			naluSize := int(binary.BigEndian.Uint16(payload[currOffset:]))
			currOffset += stapaNALULengthSize

			if len(payload) < currOffset+naluSize {
				return nil, fmt.Errorf("%w STAP-A declared size(%d) is larger than buffer(%d)", errShortPacket, naluSize, len(payload)-currOffset)
			}

			result = append(result, p.doPackaging(payload[currOffset:currOffset+naluSize])...)
			currOffset += naluSize
		}
		return result, nil

	case naluType == fuaNALUType:
		if len(payload) < fuaHeaderSize {
			return nil, errShortPacket
		}

		if p.fuaBuffer == nil {
			p.fuaBuffer = []byte{}
		}

		p.fuaBuffer = append(p.fuaBuffer, payload[fuaHeaderSize:]...)

		if payload[1]&fuEndBitmask != 0 {
			naluRefIdc := payload[0] & naluRefIdcBitmask
			fragmentedNaluType := payload[1] & naluTypeBitmask

			nalu := append([]byte{}, naluRefIdc|fragmentedNaluType)
			nalu = append(nalu, p.fuaBuffer...)
			p.fuaBuffer = nil
			return p.doPackaging(nalu), nil
		}

		return []byte{}, nil
	}

	return nil, fmt.Errorf("%w: %d", errUnhandledNALUType, naluType)
}

// H264PartitionHeadChecker is obsolete
type H264PartitionHeadChecker struct{}

// IsPartitionHead checks if this is the head of a packetized nalu stream.
func (*H264Packet) IsPartitionHead(payload []byte) bool {
	if len(payload) < 2 {
		return false
	}

	if payload[0]&naluTypeBitmask == fuaNALUType ||
		payload[0]&naluTypeBitmask == fubNALUType {
		return payload[1]&fuStartBitmask != 0
	}

	return true
}
