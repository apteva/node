package hw

import (
	"encoding/binary"
	"fmt"
	"time"
)

// BME280 reads temperature, humidity and pressure in forced mode: one
// measurement per Read, then the chip goes back to sleep. Formulas are the
// floating-point compensation from the Bosch datasheet (section 8.1).
type BME280 struct {
	dev I2CDevice
	cal bme280Cal
}

type bme280Cal struct {
	T1             uint16
	T2, T3         int16
	P1             uint16
	P2, P3, P4, P5 int16
	P6, P7, P8, P9 int16
	H1, H3         uint8
	H2, H4, H5     int16
	H6             int8
}

const (
	bmeRegChipID   = 0xD0
	bmeRegReset    = 0xE0
	bmeRegCtrlHum  = 0xF2
	bmeRegStatus   = 0xF3
	bmeRegCtrlMeas = 0xF4
	bmeRegData     = 0xF7
	bmeChipID      = 0x60
)

func NewBME280(dev I2CDevice) (*BME280, error) {
	id, err := dev.ReadReg(bmeRegChipID, 1)
	if err != nil {
		return nil, fmt.Errorf("bme280: read chip id: %w", err)
	}
	if id[0] != bmeChipID {
		return nil, fmt.Errorf("bme280: unexpected chip id 0x%02x (want 0x60)", id[0])
	}
	if err := dev.WriteReg(bmeRegReset, 0xB6); err != nil {
		return nil, err
	}
	time.Sleep(5 * time.Millisecond)

	a, err := dev.ReadReg(0x88, 26)
	if err != nil {
		return nil, err
	}
	b, err := dev.ReadReg(0xE1, 7)
	if err != nil {
		return nil, err
	}
	return &BME280{dev: dev, cal: parseBME280Cal(a, b)}, nil
}

func parseBME280Cal(a, b []byte) bme280Cal {
	le := binary.LittleEndian
	s16 := func(p []byte) int16 { return int16(le.Uint16(p)) }
	return bme280Cal{
		T1: le.Uint16(a[0:]), T2: s16(a[2:]), T3: s16(a[4:]),
		P1: le.Uint16(a[6:]), P2: s16(a[8:]), P3: s16(a[10:]), P4: s16(a[12:]),
		P5: s16(a[14:]), P6: s16(a[16:]), P7: s16(a[18:]), P8: s16(a[20:]), P9: s16(a[22:]),
		H1: a[25],
		H2: s16(b[0:]),
		H3: b[2],
		H4: int16(int8(b[3]))<<4 | int16(b[4]&0x0F),
		H5: int16(int8(b[5]))<<4 | int16(b[4]>>4),
		H6: int8(b[6]),
	}
}

func (s *BME280) Read() (map[string]float64, error) {
	if err := s.dev.WriteReg(bmeRegCtrlHum, 0x01); err != nil { // humidity x1
		return nil, err
	}
	if err := s.dev.WriteReg(bmeRegCtrlMeas, 0x25); err != nil { // temp x1, press x1, forced
		return nil, err
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		time.Sleep(10 * time.Millisecond)
		st, err := s.dev.ReadReg(bmeRegStatus, 1)
		if err != nil {
			return nil, err
		}
		if st[0]&0x08 == 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("bme280: measurement timed out")
		}
	}
	d, err := s.dev.ReadReg(bmeRegData, 8)
	if err != nil {
		return nil, err
	}
	adcP := int32(d[0])<<12 | int32(d[1])<<4 | int32(d[2])>>4
	adcT := int32(d[3])<<12 | int32(d[4])<<4 | int32(d[5])>>4
	adcH := int32(d[6])<<8 | int32(d[7])
	t, p, h := s.cal.compensate(adcT, adcP, adcH)
	return map[string]float64{
		"temperature_c": round1(t),
		"humidity_pct":  round1(h),
		"pressure_hpa":  round1(p / 100),
	}, nil
}

// compensate returns °C, Pa and %RH.
func (c bme280Cal) compensate(adcT, adcP, adcH int32) (float64, float64, float64) {
	T := float64(adcT)
	v1 := (T/16384.0 - float64(c.T1)/1024.0) * float64(c.T2)
	v2 := (T/131072.0 - float64(c.T1)/8192.0)
	v2 = v2 * v2 * float64(c.T3)
	tFine := v1 + v2
	temp := tFine / 5120.0

	var press float64
	v1 = tFine/2.0 - 64000.0
	v2 = v1 * v1 * float64(c.P6) / 32768.0
	v2 = v2 + v1*float64(c.P5)*2.0
	v2 = v2/4.0 + float64(c.P4)*65536.0
	v1 = (float64(c.P3)*v1*v1/524288.0 + float64(c.P2)*v1) / 524288.0
	v1 = (1.0 + v1/32768.0) * float64(c.P1)
	if v1 != 0 {
		p := 1048576.0 - float64(adcP)
		p = (p - v2/4096.0) * 6250.0 / v1
		v1 = float64(c.P9) * p * p / 2147483648.0
		v2 = p * float64(c.P8) / 32768.0
		press = p + (v1+v2+float64(c.P7))/16.0
	}

	h := tFine - 76800.0
	h = (float64(adcH) - (float64(c.H4)*64.0 + float64(c.H5)/16384.0*h)) *
		(float64(c.H2) / 65536.0 * (1.0 + float64(c.H6)/67108864.0*h*(1.0+float64(c.H3)/67108864.0*h)))
	h = h * (1.0 - float64(c.H1)*h/524288.0)
	h = min(max(h, 0), 100)

	return temp, press, h
}

func (s *BME280) Close() error { return s.dev.Close() }
