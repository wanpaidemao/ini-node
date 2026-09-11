package main

// Upnp code taken from Taipei Torrent license is below:
// Copyright (c) 2010 Jack Palevich. All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//    * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//    * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//    * Neither the name of Google Inc. nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

// Just enough UPnP to be able to forward ports
//

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// NAT is an interface representing a NAT traversal options for example UPNP or
// NAT-PMP. It provides methods to query and manipulate this traversal to allow
// access to services.
type NAT interface {
	// Get the external address from outside the NAT.
	GetExternalAddress() (addr net.IP, err error)
	// Add a port mapping for protocol ("udp" or "tcp") from external port to
	// internal port with description lasting for timeout.
	AddPortMapping(protocol string, externalPort, internalPort int, description string, timeout int) (mappedExternalPort int, err error)
	// Remove a previously added port mapping from external port to
	// internal port.
	DeletePortMapping(protocol string, externalPort, internalPort int) (err error)
}

type upnpNAT struct {
	serviceURL string
	ourIP      string
}

// Discover searches the local network for a UPnP router returning a NAT
// for the network if so, nil if not.
func Discover() (nat NAT, err error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return
	}
	socket := conn.(*net.UDPConn)
	defer socket.Close()

	// Standard multicast SSDP discovery (239.255.255.250:1900).
	ssdp := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	nat, err = discoverProbe(socket, ssdp)
	if err == nil {
		return
	}

	// Some routers (e.g. OpenWrt with MiniUPnPd) disable multicast SSDP
	// responses entirely but still answer a unicast M-SEARCH — fall back to
	// probing the default gateway directly. / 部分路由器(MiniUPnPd 系)完全
	// 不响应组播 SSDP 但会应答单播 M-SEARCH——回退到直连默认网关探测。
	if gw := defaultGatewayIPv4(); gw != nil {
		unicast := &net.UDPAddr{IP: gw, Port: 1900}
		if nat, err = discoverProbe(socket, unicast); err == nil {
			return
		}
	}

	err = errors.New("UPnP port discovery failed")
	return
}

// discoverProbe sends repeated M-SEARCH probes to target (multicast or
// unicast) and parses the first matching IGD response into a NAT.
func discoverProbe(socket *net.UDPConn, target *net.UDPAddr) (nat NAT, err error) {
	err = socket.SetDeadline(time.Now().Add(3 * time.Second))
	if err != nil {
		return
	}

	st := "ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n"
	buf := bytes.NewBufferString(
		"M-SEARCH * HTTP/1.1\r\n" +
			"HOST: " + target.String() + "\r\n" +
			st +
			"MAN: \"ssdp:discover\"\r\n" +
			"MX: 2\r\n\r\n")
	message := buf.Bytes()
	answerBytes := make([]byte, 1024)
	for i := 0; i < 3; i++ {
		_, err = socket.WriteToUDP(message, target)
		if err != nil {
			return
		}
		var n int
		n, _, err = socket.ReadFromUDP(answerBytes)
		if err != nil {
			continue
		}
		answer := string(answerBytes[0:n])
		if !strings.Contains(answer, "\r\n"+st) {
			continue
		}
		// HTTP header field names are case-insensitive.
		// http://www.w3.org/Protocols/rfc2616/rfc2616-sec4.html#sec4.2
		locString := "\r\nlocation: "
		locIndex := strings.Index(strings.ToLower(answer), locString)
		if locIndex < 0 {
			continue
		}
		loc := answer[locIndex+len(locString):]
		endIndex := strings.Index(loc, "\r\n")
		if endIndex < 0 {
			continue
		}
		locURL := loc[0:endIndex]
		var serviceURL string
		serviceURL, err = getServiceURL(locURL)
		if err != nil {
			return
		}
		var ourIP string
		ourIP, err = getOurIP()
		if err != nil {
			return
		}
		nat = &upnpNAT{serviceURL: serviceURL, ourIP: ourIP}
		return
	}
	err = errors.New("UPnP port discovery failed")
	return
}

// service represents the Service type in an UPnP xml description.
// Only the parts we care about are present and thus the xml may have more
// fields than present in the structure.
type service struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

// deviceList represents the deviceList type in an UPnP xml description.
// Only the parts we care about are present and thus the xml may have more
// fields than present in the structure.
type deviceList struct {
	XMLName xml.Name `xml:"deviceList"`
	Device  []device `xml:"device"`
}

// serviceList represents the serviceList type in an UPnP xml description.
// Only the parts we care about are present and thus the xml may have more
// fields than present in the structure.
type serviceList struct {
	XMLName xml.Name  `xml:"serviceList"`
	Service []service `xml:"service"`
}

// device represents the device type in an UPnP xml description.
// Only the parts we care about are present and thus the xml may have more
// fields than present in the structure.
type device struct {
	XMLName     xml.Name    `xml:"device"`
	DeviceType  string      `xml:"deviceType"`
	DeviceList  deviceList  `xml:"deviceList"`
	ServiceList serviceList `xml:"serviceList"`
}

// specVersion represents the specVersion in a UPnP xml description.
// Only the parts we care about are present and thus the xml may have more
// fields than present in the structure.
type specVersion struct {
	XMLName xml.Name `xml:"specVersion"`
	Major   int      `xml:"major"`
	Minor   int      `xml:"minor"`
}

// root represents the Root document for a UPnP xml description.
// Only the parts we care about are present and thus the xml may have more
// fields than present in the structure.
type root struct {
	XMLName     xml.Name `xml:"root"`
	SpecVersion specVersion
	Device      device
}

// getChildDevice searches the children of device for a device with the given
// type.
func getChildDevice(d *device, deviceType string) *device {
	for i := range d.DeviceList.Device {
		if d.DeviceList.Device[i].DeviceType == deviceType {
			return &d.DeviceList.Device[i]
		}
	}
	return nil
}

// getChildService searches the service list of device for a service with the
// given type.
func getChildService(d *device, serviceType string) *service {
	for i := range d.ServiceList.Service {
		if d.ServiceList.Service[i].ServiceType == serviceType {
			return &d.ServiceList.Service[i]
		}
	}
	return nil
}

// defaultGatewayIPv4 returns the IPv4 address of this host's default gateway,
// or nil when it cannot be determined.  Used as the fallback unicast target
// for UPnP/SSDP discovery when the router ignores multicast M-SEARCH probes
// (e.g. OpenWrt MiniUPnPd).
// defaultGatewayIPv4 返回本机默认网关的 IPv4 地址,无法确定时返回 nil。
// 作为路由器忽略组播 M-SEARCH 时(如 OpenWrt MiniUPnPd)UPnP/SSDP 发现的
// 单播回退探测目标。
func defaultGatewayIPv4() net.IP {
	switch runtime.GOOS {
	case "windows":
		// "route print -4 0.0.0.0" prints the default route as
		// "0.0.0.0  0.0.0.0  192.168.68.1  192.168.68.2  25"; match by the
		// numeric destination/mask so parsing is locale-independent.
		// 默认路由行形如 "0.0.0.0  0.0.0.0  192.168.68.1  192.168.68.2  25",
		// 按数字键值匹配,与系统语言无关。
		out, err := exec.Command("route", "print", "-4", "0.0.0.0").Output()
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(out), "\r\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[0] == "0.0.0.0" && f[1] == "0.0.0.0" {
				if ip := net.ParseIP(f[2]); ip != nil {
					return ip
				}
			}
		}
	case "linux":
		// /proc/net/route stores destination/gateway as little-endian hex.
		// /proc/net/route 中目的/网关为小端十六进制。
		b, rerr := os.ReadFile("/proc/net/route")
		if rerr != nil {
			return nil
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) >= 3 && f[1] == "00000000" {
				gw, perr := strconv.ParseUint(f[2], 16, 32)
				if perr != nil {
					continue
				}
				return net.IPv4(byte(gw), byte(gw>>8), byte(gw>>16),
					byte(gw>>24))
			}
		}
	default: // darwin and other BSDs
		// "route -n get default" prints "gateway: 192.168.68.1".
		// 输出形如 "gateway: 192.168.68.1"。
		out, err := exec.Command("route", "-n", "get", "default").Output()
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == "gateway:" {
				if ip := net.ParseIP(f[1]); ip != nil {
					return ip
				}
			}
		}
	}
	return nil
}

// getOurIP returns a best guess at the IPv4 address of this host.  The
// historical implementation derived it from net.LookupCNAME(hostname), which
// fails whenever the unqualified host name cannot be resolved by the system
// resolver (common on Windows); enumerating the interfaces instead never
// depends on DNS.  Prefer the address on the default gateway's subnet, since
// that is the one the router will see as NewInternalClient.
// getOurIP 返回本机 IPv4 地址的最佳猜测。旧实现用 net.LookupCNAME(hostname)
// 推导,当主机名无法被系统解析器解析时(Windows 上很常见)会直接失败;这里
// 改为枚举网卡,完全不依赖 DNS。优先返回与默认网关同网段的地址——那才是
// 路由器会当作 NewInternalClient 看到的地址。
func getOurIP() (ip string, err error) {
	gw := defaultGatewayIPv4()
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	// First pass: an IPv4 on an up, non-loopback interface that shares the
	// default gateway's subnet.
	// 第一遍:处于默认网关同网段、且网卡为 up 的非回环 IPv4。
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, aerr := ifi.Addrs()
		if aerr != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() ||
				ip4.IsLinkLocalUnicast() {
				continue
			}
			if gw != nil && ipNet.Contains(gw) {
				return ip4.String(), nil
			}
		}
	}
	// Fallback: any usable IPv4.
	// 兜底:任意可用 IPv4。
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, aerr := ifi.Addrs()
		if aerr != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() ||
				ip4.IsLinkLocalUnicast() {
				continue
			}
			return ip4.String(), nil
		}
	}
	return "", errors.New("no suitable IPv4 address found")
}

// getServiceURL parses the xml description at the given root url to find the
// url for the WANIPConnection service to be used for port forwarding.
func getServiceURL(rootURL string) (url string, err error) {
	r, err := http.Get(rootURL)
	if err != nil {
		return
	}
	defer r.Body.Close()
	if r.StatusCode >= 400 {
		err = errors.New(fmt.Sprint(r.StatusCode))
		return
	}
	var root root
	err = xml.NewDecoder(r.Body).Decode(&root)
	if err != nil {
		return
	}
	a := &root.Device
	if a.DeviceType != "urn:schemas-upnp-org:device:InternetGatewayDevice:1" {
		err = errors.New("no InternetGatewayDevice")
		return
	}
	b := getChildDevice(a, "urn:schemas-upnp-org:device:WANDevice:1")
	if b == nil {
		err = errors.New("no WANDevice")
		return
	}
	c := getChildDevice(b, "urn:schemas-upnp-org:device:WANConnectionDevice:1")
	if c == nil {
		err = errors.New("no WANConnectionDevice")
		return
	}
	d := getChildService(c, "urn:schemas-upnp-org:service:WANIPConnection:1")
	if d == nil {
		err = errors.New("no WANIPConnection")
		return
	}
	url = combineURL(rootURL, d.ControlURL)
	return
}

// combineURL appends subURL onto rootURL.
func combineURL(rootURL, subURL string) string {
	protocolEnd := "://"
	protoEndIndex := strings.Index(rootURL, protocolEnd)
	a := rootURL[protoEndIndex+len(protocolEnd):]
	rootIndex := strings.Index(a, "/")
	return rootURL[0:protoEndIndex+len(protocolEnd)+rootIndex] + subURL
}

// soapBody represents the <s:Body> element in a SOAP reply.
// fields we don't care about are elided.
type soapBody struct {
	XMLName xml.Name `xml:"Body"`
	Data    []byte   `xml:",innerxml"`
}

// soapEnvelope represents the <s:Envelope> element in a SOAP reply.
// fields we don't care about are elided.
type soapEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    soapBody `xml:"Body"`
}

// soapRequests performs a soap request with the given parameters and returns
// the xml replied stripped of the soap headers. in the case that the request is
// unsuccessful the an error is returned.
func soapRequest(url, function, message string) (replyXML []byte, err error) {
	fullMessage := "<?xml version=\"1.0\" ?>" +
		"<s:Envelope xmlns:s=\"http://schemas.xmlsoap.org/soap/envelope/\" s:encodingStyle=\"http://schemas.xmlsoap.org/soap/encoding/\">\r\n" +
		"<s:Body>" + message + "</s:Body></s:Envelope>"

	req, err := http.NewRequest("POST", url, strings.NewReader(fullMessage))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml ; charset=\"utf-8\"")
	req.Header.Set("User-Agent", "Darwin/10.0.0, UPnP/1.0, MiniUPnPc/1.3")
	req.Header.Set("SOAPAction", "\"urn:schemas-upnp-org:service:WANIPConnection:1#"+function+"\"")
	req.Header.Set("Connection", "Close")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if r.Body != nil {
		defer r.Body.Close()
	}

	if r.StatusCode >= 400 {
		err = errors.New("Error " + strconv.Itoa(r.StatusCode) + " for " + function)
		r = nil
		return
	}
	var reply soapEnvelope
	err = xml.NewDecoder(r.Body).Decode(&reply)
	if err != nil {
		return nil, err
	}
	return reply.Body.Data, nil
}

// getExternalIPAddressResponse represents the XML response to a
// GetExternalIPAddress SOAP request.
type getExternalIPAddressResponse struct {
	XMLName           xml.Name `xml:"GetExternalIPAddressResponse"`
	ExternalIPAddress string   `xml:"NewExternalIPAddress"`
}

// GetExternalAddress implements the NAT interface by fetching the external IP
// from the UPnP router.
func (n *upnpNAT) GetExternalAddress() (addr net.IP, err error) {
	message := "<u:GetExternalIPAddress xmlns:u=\"urn:schemas-upnp-org:service:WANIPConnection:1\"/>\r\n"
	response, err := soapRequest(n.serviceURL, "GetExternalIPAddress", message)
	if err != nil {
		return nil, err
	}

	var reply getExternalIPAddressResponse
	err = xml.Unmarshal(response, &reply)
	if err != nil {
		return nil, err
	}

	addr = net.ParseIP(reply.ExternalIPAddress)
	if addr == nil {
		return nil, errors.New("unable to parse ip address")
	}
	return addr, nil
}

// AddPortMapping implements the NAT interface by setting up a port forwarding
// from the UPnP router to the local machine with the given ports and protocol.
func (n *upnpNAT) AddPortMapping(protocol string, externalPort, internalPort int, description string, timeout int) (mappedExternalPort int, err error) {
	// A single concatenation would break ARM compilation.
	message := "<u:AddPortMapping xmlns:u=\"urn:schemas-upnp-org:service:WANIPConnection:1\">\r\n" +
		"<NewRemoteHost></NewRemoteHost><NewExternalPort>" + strconv.Itoa(externalPort)
	message += "</NewExternalPort><NewProtocol>" + strings.ToUpper(protocol) + "</NewProtocol>"
	message += "<NewInternalPort>" + strconv.Itoa(internalPort) + "</NewInternalPort>" +
		"<NewInternalClient>" + n.ourIP + "</NewInternalClient>" +
		"<NewEnabled>1</NewEnabled><NewPortMappingDescription>"
	message += description +
		"</NewPortMappingDescription><NewLeaseDuration>" + strconv.Itoa(timeout) +
		"</NewLeaseDuration></u:AddPortMapping>"

	response, err := soapRequest(n.serviceURL, "AddPortMapping", message)
	if err != nil {
		return
	}

	// TODO: check response to see if the port was forwarded
	// If the port was not wildcard we don't get an reply with the port in
	// it. Not sure about wildcard yet. miniupnpc just checks for error
	// codes here.
	mappedExternalPort = externalPort
	_ = response
	return
}

// DeletePortMapping implements the NAT interface by removing up a port forwarding
// from the UPnP router to the local machine with the given ports and.
func (n *upnpNAT) DeletePortMapping(protocol string, externalPort, internalPort int) (err error) {

	message := "<u:DeletePortMapping xmlns:u=\"urn:schemas-upnp-org:service:WANIPConnection:1\">\r\n" +
		"<NewRemoteHost></NewRemoteHost><NewExternalPort>" + strconv.Itoa(externalPort) +
		"</NewExternalPort><NewProtocol>" + strings.ToUpper(protocol) + "</NewProtocol>" +
		"</u:DeletePortMapping>"

	response, err := soapRequest(n.serviceURL, "DeletePortMapping", message)
	if err != nil {
		return
	}

	// TODO: check response to see if the port was deleted
	// log.Println(message, response)
	_ = response
	return
}
