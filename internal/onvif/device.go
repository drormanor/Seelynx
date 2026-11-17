package onvif

import (
	"context"
	"strings"
)

// DeviceInfo is what a camera says it is. The serial number is left out on purpose.
type DeviceInfo struct {
	Manufacturer string
	Model        string
	Firmware     string
}

// DeviceInformation reads the camera's make, model and firmware.
func (c *Client) DeviceInformation(ctx context.Context) (DeviceInfo, error) {
	var out struct {
		Manufacturer string `xml:"Body>GetDeviceInformationResponse>Manufacturer"`
		Model        string `xml:"Body>GetDeviceInformationResponse>Model"`
		Firmware     string `xml:"Body>GetDeviceInformationResponse>FirmwareVersion"`
	}
	err := c.call(ctx, c.XAddr, `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`, &out)
	return DeviceInfo{strings.TrimSpace(out.Manufacturer), strings.TrimSpace(out.Model), strings.TrimSpace(out.Firmware)}, err
}
