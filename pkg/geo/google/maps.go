package google

import (
	"bytes"
	"github.com/gordy96/fb_parser/pkg/fb/util"
	"github.com/gordy96/fb_parser/pkg/geo"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
)

type CannotParseError struct {
	Query    string
	Request  []byte
	Response []byte
}

func (c CannotParseError) Error() string {
	return "can not parse response"
}

type MapsHttp struct {
}

var ProxyString string = ""

func (m MapsHttp) FindByName(name string) (geo.Point, error) {
	req, _ := http.NewRequest("GET", "https://www.google.com/maps/search/", nil)
	q := url.Values{}
	q.Set("api", "1")
	q.Set("hl", "en")
	q.Set("query", name)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Connection", "close")
	var cl http.Client
	if ProxyString != "" {
		proxyUrl, _ := url.Parse(ProxyString)
		cl = http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyUrl),
			},
		}
	}
	resp, err := cl.Do(req)
	req.Close = true
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	point := geo.Point{}

	if err != nil {
		return point, err
	}

	re := regexp.MustCompile("center=(-?[0-9]+?\\.[0-9]+).+?(-?[0-9]+?\\.[0-9]+)")
	buf := util.ReadAll(resp)
	grep := re.FindAllSubmatch(buf, 1)
	if grep == nil || len(grep) < 1 || len(grep[0]) < 3 {
		return point, generateParseError(name, req, resp, buf)
	}
	x, err := strconv.ParseFloat(string(grep[0][2]), 64)
	if err != nil {
		return point, generateParseError(name, req, resp, buf)
	}
	y, err := strconv.ParseFloat(string(grep[0][1]), 64)
	if err != nil {
		return point, generateParseError(name, req, resp, buf)
	}
	point.X = x
	point.Y = y
	return point, nil
}

func generateParseError(name string, req *http.Request, resp *http.Response, buf []byte) *CannotParseError {
	resp.Body.Close()
	resp.Body = ioutil.NopCloser(bytes.NewBuffer(buf))
	rawReq, _ := httputil.DumpRequestOut(req, true)
	rawResp, _ := httputil.DumpResponse(resp, true)
	return &CannotParseError{name, rawReq, rawResp}
}
