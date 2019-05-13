package util

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

func MakeRequest(method string, uri string, body io.Reader) *http.Request {
	req, _ := http.NewRequest(method, uri, body)
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU OS 10_14_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/11.1.1 Mobile/14E304 Safari/605.1.15")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-GB,en-US;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "deflate")
	return req
}

func ReadAll(response *http.Response) []byte {
	ret, _ := ioutil.ReadAll(response.Body)
	return ret
}

func ParseFromSource(r string, source []byte) []byte {
	re := regexp.MustCompile(r)
	ret := re.FindSubmatch(source)
	if ret == nil || len(ret) < 2 {
		return nil
	}
	return ret[1]
}

func parseCookieFromSource(r string, source []byte) [][]byte {
	re := regexp.MustCompile(r)
	return re.FindSubmatch(source)
}

func GrepFormValue(name string, source []byte) []byte {
	return ParseFromSource(fmt.Sprintf("name=\"%s\" value=\"([^\"]+)\"", name), source)
}

func GetDTSG(source []byte) []byte {
	return ParseFromSource("dtsg\".*?token\"[^\"]+?\"([^\"]+?)\"", source)
}

func GetDTSGAG(source []byte) []byte {
	return ParseFromSource("dtsg_ag\".*?token\"[^\"]+?\"([^\"]+?)\"", source)
}

func GetAJAX(source []byte) []byte {
	return ParseFromSource("ajaxResponseToken.*encrypted\":\"([^\"]+)\"", source)
}

func getCookie(name string, source []byte, resp *http.Response) *http.Cookie {
	if resp != nil {
		for _, c := range resp.Cookies() {
			if c.Name == name {
				return c
			}
		}
	}
	parts := parseCookieFromSource(fmt.Sprintf("_js_%s\",\"([^\"]+)\",([^,]+)", name), source)

	i, err := strconv.ParseInt(string(parts[2]), 10, 64)
	if err != nil {
		panic(err)
	}
	tm := time.Unix(i, 0)

	return &http.Cookie{
		Name: name,
		Value: string(parts[1]),
		Path: "/",
		Expires: tm,
		Domain: ".facebook.com",
	}
}

func GetFRCookie(source []byte, resp *http.Response) *http.Cookie {
	return getCookie("fr", source, resp)
}

func GetSBCookie(source []byte, resp *http.Response) *http.Cookie {
	return getCookie("sb", source, resp)
}

func GetDATRCookie(source []byte, resp *http.Response) *http.Cookie {
	return getCookie("datr", source, resp)
}

func GetRefID(source []byte) []byte {
	return ParseFromSource("refid=([0-9]+)", source)
}
