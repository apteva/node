package hw

import (
	"math"
	"testing"
)

// Worked example from the Bosch BMP280 datasheet (section 3.12); the BME280
// shares the temperature and pressure compensation.
func TestBME280CompensateDatasheetVector(t *testing.T) {
	c := bme280Cal{
		T1: 27504, T2: 26435, T3: -1000,
		P1: 36477, P2: -10685, P3: 3024, P4: 2855, P5: 140, P6: -7, P7: 15500, P8: -14600, P9: 6000,
		H1: 75, H2: 362, H3: 0, H4: 324, H5: 50, H6: 30,
	}
	temp, press, hum := c.compensate(519888, 415148, 30000)
	if math.Abs(temp-25.08) > 0.01 {
		t.Errorf("temperature = %.3f, want 25.08", temp)
	}
	if math.Abs(press-100653.27) > 1 {
		t.Errorf("pressure = %.2f Pa, want ~100653", press)
	}
	if hum < 0 || hum > 100 {
		t.Errorf("humidity %.2f out of range", hum)
	}
}

func TestParseBME280CalSplitsH4H5Nibbles(t *testing.T) {
	a := make([]byte, 26)
	b := []byte{0, 0, 0, 0x14, 0xA3, 0x05, 0}
	c := parseBME280Cal(a, b)
	if c.H4 != 0x14<<4|0x3 {
		t.Errorf("H4 = %d", c.H4)
	}
	if c.H5 != 0x05<<4|0xA {
		t.Errorf("H5 = %d", c.H5)
	}
}
