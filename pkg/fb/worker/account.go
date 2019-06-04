package worker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gordy96/fb_parser/pkg/fb"
	"github.com/gordy96/fb_parser/pkg/fb/place"
	"github.com/gordy96/fb_parser/pkg/fb/util"
	"github.com/gordy96/fb_parser/pkg/fb/worker/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	loginFields = []string{"had_cp_prefilled", "had_password_prefilled", "m_sess", "jazoest", "__req", "__ajax__", "li", "unrecognized_tries", "email", "prefill_type", "fb_dtsg", "m_ts", "try_number", "prefill_source", "first_prefill_source", "lsd", "__user", "__dyn", "pass", "prefill_contact_point", "first_prefill_type", "is_smart_lock"}
)

type Status string

const (
	Available Status = "available"
	Busy      Status = "busy"
	Error     Status = "error"
	Resting   Status = "resting"
)

type FBAccount struct {
	Email          string        `json:"email" bson:"email"`
	Password       string        `json:"password" bson:"password"`
	Proxy          string        `json:"proxy" bson:"proxy"`
	Env            Env           `json:"-" bson:"env"`
	Status         Status `json:"status" bson:"status"`
	RequestTimeout int           `json:"request_timeout" bson:"request_timeout"`
	ReleaseTimeout int           `json:"release_timeout" bson:"release_timeout"`
	CreatedAt      int64         `json:"created_at" bson:"created_at"`
	cl             http.Client   `json:"-" bson:"-"`
}

func NewFBAccount(email, password string, proxy string) *FBAccount {
	acc := &FBAccount{
		Password: password,
		Email:    email,
		Proxy:    proxy,
		Status:   Available,
	}
	acc.Init()
	return acc
}

func (a *FBAccount) Init() {
	jar, _ := cookiejar.New(nil)
	a.cl = http.Client{
		Jar: jar,
	}
	if a.Proxy != "" {
		proxyUrl, _ := url.Parse(a.Proxy)
		a.cl.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxyUrl),
		}
	}
	u, _ := url.Parse("https://m.facebook.com")
	a.cl.Jar.SetCookies(u, a.Env.Cookies)
}

func handleResponseBasedErrors(resp *http.Response) ([]byte, error) {
	respBuf := util.ReadAll(resp)
	/*||
	strings.Index(string(respBuf), "Content Not Found") > 0*/
	if strings.Index(string(respBuf), "Just a few more steps before you log in") > 0 ||
		strings.Index(string(respBuf), "MCheckpointRedirect") > 0 {
		err := errors.WorkerCheckpointError{}
		rawReq, _ := httputil.DumpRequestOut(resp.Request, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		err.Request = rawReq
		err.Response = append(rawResp, respBuf...)
		return nil, err
	} else if strings.Index(string(respBuf), "link you followed may be broken, or the page may have been removed") > 0 {
		err := errors.WorkerCheckpointError{}
		rawReq, _ := httputil.DumpRequestOut(resp.Request, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		err.Request = rawReq
		err.Response = append(rawResp, respBuf...)
		return nil, err
	}

	return respBuf, nil
}

func (a *FBAccount) do(req *http.Request) (*http.Response, error) {
	resp, err := a.cl.Do(req)
	req.Close = true
	return resp, err
}

func (a *FBAccount) Login() error {
	var req *http.Request
	var resp *http.Response
	var err error
	req = util.MakeRequest("GET", util.Host, nil)
	resp, err = a.do(req)
	if err != nil {
		panic(err)
	}

	a.Env = MakeEnv(resp, a.Email+":"+a.Password)
	req = util.MakeRequest(
		"POST",
		util.Host+"/login/device-based/login/async/?refsrc=https%3A%2F%2Fwww.facebook.com%2F&lwv=100&jio_prefilled=false",
		strings.NewReader(a.Env.MakeBody(loginFields).Encode()))

	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	u, err := url.Parse("https://m.facebook.com")
	a.cl.Jar, _ = cookiejar.New(nil)
	a.cl.Jar.SetCookies(u, a.Env.Cookies)
	resp, err = a.do(req)

	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return err
	}
	uid := func(cookies []*http.Cookie) string {
		for _, c := range cookies {
			if c.Name == "c_user" {
				return c.Value
			}
		}
		return ""
	}(resp.Cookies())

	if uid == "" {
		rawReq, _ := httputil.DumpRequestOut(req, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		return errors.AuthenticationFailedError{Request: rawReq, Response: rawResp}
	}

	a.Env.Increment()
	a.Env.Set("__user", uid)

	req = util.MakeRequest("GET", util.Host+"/login/save-device/?login_source=login", nil)

	resp, err = a.do(req)

	if err != nil {
		return err
	}

	retBuf := util.ReadAll(resp)
	dtsgag := util.GetDTSGAG(retBuf)

	a.Env.Set("__ajax__", string(util.GetAJAX(retBuf)))
	a.Env.Set("fb_dtsg_ag", string(dtsgag))
	a.Env.Set("jazoest", jazoest(dtsgag))

	dtsg := util.ParseFromSource("fb_dtsg.*?value=\\\"([^\"]+)", retBuf)
	if dtsg != nil {
		a.Env.Set("fb_dtsg", string(dtsg))
	}

	if locale := findLocale(retBuf); locale != "en_US" {
		err = a.SetLocale("en_US")
		if err != nil {
			return err
		}
	}

	a.Env.Cookies = a.cl.Jar.Cookies(u)

	return nil
}

func findLocale(buf []byte) string {
	re := regexp.MustCompile("\\/([a-z]{2}_[A-Z]{2})\\/")
	matches := re.FindSubmatch(buf)
	if len(matches) > 1 {
		return string(matches[1])
	}
	return ""
}

func (a *FBAccount) SetLocale(locale string) error {
	var req *http.Request
	var resp *http.Response
	var err error

	var q url.Values

	q = a.Env.MakeBody([]string{
		"fb_dtsg",
		"jazoest",
		"__dyn",
		"__req",
		"__ajax__",
		"__user",
	})

	q.Set("m_sess", "")
	q.Set("should_redirect", "false")
	q.Set("ref", "m_touch_locale_selector")
	q.Set("__m_async_page__", "")
	q.Set("loc", locale)
	q.Set("jazoest", jazoest([]byte(q.Get("fb_dtsg"))))

	referer := "/language.php"

	r := new(bytes.Buffer)
	encoder := json.NewEncoder(r)
	encoder.SetEscapeHTML(false)
	m := map[string]string{
		"s": "m",
		"r": referer,
		"h": referer,
	}

	encoder.Encode(&m)

	req = util.MakeRequest("POST", util.Host+"/intl/ajax/save_locale/", strings.NewReader(q.Encode()))
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	req.AddCookie(&http.Cookie{
		Name:   "x-referer",
		Value:  base64.RawStdEncoding.EncodeToString(r.Bytes()),
		Path:   "/",
		Domain: ".facebook.com",
	})

	req.URL.RawQuery = q.Encode()
	req.Header.Set("Referer", util.Host+referer)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Response-Format", "JSONStream")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err = a.do(req)

	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return err
	}

	_, err = handleResponseBasedErrors(resp)

	if err != nil {
		return err
	}
	return nil
}

func (a *FBAccount) GetUserInfo(user *fb.Account) error {
	var req *http.Request
	var resp *http.Response
	var err error

	if user.ID == "" {
		if user.Nickname == "" {
			return fb.MalformedAccountError{Account: user}
		}
		id, err := a.GetIDFromNickname(user.Nickname)
		if err != nil {
			return err
		}
		if id == "" {
			return fb.MalformedAccountError{Account: user}
		}
		user.ID = id
	}

	var q url.Values

	req = util.MakeRequest("GET", util.Host+"/profile.php", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	ctime := strconv.FormatInt(time.Now().Unix(), 10)
	a.Env.Set("ctime", ctime)
	q = a.Env.MakeBody([]string{
		"fb_dtsg_ag",
		"jazoest",
		"__dyn",
		"__req",
		"__ajax__",
		"__user",
	})

	q.Set("m_sess", "")
	q.Set("__disable_brotli__", "")
	q.Set("__big_pipe_on__", "")
	q.Set("__m_async_page__", "")
	q.Set("lst", fmt.Sprintf("%s:%s:%s", q.Get("__user"), user.ID, ctime))
	q.Set("id", user.ID)
	q.Set("v", "info")
	q.Set("jazoest", jazoest([]byte(q.Get("fb_dtsg_ag"))))

	sq := url.Values{}

	sq.Set("lst", q.Get("lst"))
	sq.Set("v", "info")
	referer := "/profile.php?" + sq.Encode()

	r := new(bytes.Buffer)
	encoder := json.NewEncoder(r)
	encoder.SetEscapeHTML(false)
	m := map[string]string{
		"s": "m",
		"r": referer,
		"h": referer,
	}
	encoder.Encode(&m)

	req.AddCookie(&http.Cookie{
		Name:   "x-referer",
		Value:  base64.RawStdEncoding.EncodeToString(r.Bytes()),
		Path:   "/",
		Domain: ".facebook.com",
	})

	req.URL.RawQuery = q.Encode()
	req.Header.Set("Referer", util.Host+referer)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Response-Format", "JSONStream")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err = a.do(req)

	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return err
	}

	responseContent, err := handleResponseBasedErrors(resp)

	if err != nil {
		return err
	}

	//Getting name
	fn := parseFullName(responseContent)

	if fn == "" {
		resp.Body.Close()
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(responseContent))
		rawReq, _ := httputil.DumpRequestOut(req, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		return errors.ParsingError{
			Stage:    fmt.Sprintf("GetUserInfo (parsing name for %s[%s])", user.Nickname, user.ID),
			Request:  rawReq,
			Response: rawResp,
		}
	}

	parts := strings.SplitN(fn, " ", 2)

	user.FirstName = parts[0]
	if len(parts) > 1 {
		user.LastName = parts[1]
	}

	//Getting gender
	g := parseGender(responseContent)
	if g < 0 {
		resp.Body.Close()
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(responseContent))
		rawReq, _ := httputil.DumpRequestOut(req, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		return errors.GenderUndefinedError{
			Stage:    fmt.Sprintf("GetUserInfo (parsing gender for %s[%s])", user.Nickname, user.ID),
			Request:  rawReq,
			Response: rawResp,
		}
	}

	user.Gender = g

	//Parse places

	places := parsePlaces(responseContent)
	if len(places) > 0 {
		user.Places = make([]primitive.ObjectID, len(places))
		for i, p := range places {
			user.Places[i] = ResolvePlace(p).ID
		}
		ht := ResolvePlace(parseHometown(responseContent))
		user.Hometown = ht.ID
		cc := ResolvePlace(parseCurrentCity(responseContent))
		user.CurrentCity = cc.ID
	}

	user.Nickname = parseNickName(responseContent)

	return nil
}

func (a *FBAccount) GetUserAlbums(user *fb.Account) ([]string, error) {
	if user.ID == "" {
		return nil, fb.MalformedAccountError{Account: user}
	}

	var req *http.Request
	var resp *http.Response
	var err error
	var q url.Values

	req = util.MakeRequest("GET", util.Host+"/profile.php", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	ctime := strconv.FormatInt(time.Now().Unix(), 10)
	a.Env.Set("ctime", ctime)
	q = a.Env.MakeBody([]string{
		"fb_dtsg_ag",
		"jazoest",
		"__dyn",
		"__req",
		"__ajax__",
		"__user",
	})

	q.Set("m_sess", "")
	q.Set("__disable_brotli__", "")
	q.Set("__big_pipe_on__", "")
	q.Set("__m_async_page__", "")
	q.Set("lst", fmt.Sprintf("%s:%s:%s", q.Get("__user"), user.ID, ctime))
	q.Set("id", user.ID)
	q.Set("v", "photos")
	q.Set("jazoest", jazoest([]byte(q.Get("fb_dtsg_ag"))))

	sq := url.Values{}

	sq.Set("lst", q.Get("lst"))
	sq.Set("v", "photos")
	referer := "/profile.php?" + sq.Encode()

	r := new(bytes.Buffer)
	encoder := json.NewEncoder(r)
	encoder.SetEscapeHTML(false)
	m := map[string]string{
		"s": "m",
		"r": referer,
		"h": referer,
	}
	encoder.Encode(&m)

	req.AddCookie(&http.Cookie{
		Name:   "x-referer",
		Value:  base64.RawStdEncoding.EncodeToString(r.Bytes()),
		Path:   "/",
		Domain: ".facebook.com",
	})

	req.URL.RawQuery = q.Encode()
	req.Header.Set("Referer", util.Host+referer)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Response-Format", "JSONStream")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err = a.do(req)

	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return nil, err
	}

	responseContent, err := handleResponseBasedErrors(resp)

	if err != nil {
		return nil, err
	}

	albums := parseUserAlbumIDs(responseContent)

	if albums == nil {
		resp.Body.Close()
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(responseContent))
		rawReq, _ := httputil.DumpRequestOut(req, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		err = errors.NoAlbumsError{
			Stage:    fmt.Sprintf("GetUserAlbums (parsing albums for %s[%s])", user.Nickname, user.ID),
			Request:  rawReq,
			Response: rawResp,
		}
		return nil, err
	}

	return albums, nil
}

func (a *FBAccount) GetUserFriendsList(user *fb.Account, cursor string) ([]fb.Account, string, error) {
	var req *http.Request
	var resp *http.Response
	var err error

	if user.ID == "" {
		return nil, "", fb.MalformedAccountError{Account: user}
	}

	var q url.Values
	if cursor == "" {
		req = util.MakeRequest("GET", util.Host+"/profile.php", nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		ctime := strconv.FormatInt(time.Now().Unix(), 10)
		a.Env.Set("ctime", ctime)
		q = a.Env.MakeBody([]string{
			"fb_dtsg_ag",
			"jazoest",
			"__dyn",
			"__req",
			"__ajax__",
			"__user",
		})

		q.Set("m_sess", "")
		q.Set("__disable_brotli__", "")
		q.Set("__big_pipe_on__", "")
		q.Set("__m_async_page__", "")
		q.Set("lst", fmt.Sprintf("%s:%s:%s", q.Get("__user"), user.ID, ctime))
		q.Set("id", user.ID)

	} else {
		b := a.Env.MakeBody([]string{
			"fb_dtsg",
			"jazoest",
			"__dyn",
			"__req",
			"__ajax__",
			"__user",
		})

		b.Set("m_sess", "")
		b.Set("jazoest", jazoest([]byte(b.Get("fb_dtsg"))))

		req = util.MakeRequest("POST", util.Host+"/profile.php", strings.NewReader(b.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		q = url.Values{}
		q.Set("id", user.ID)
		ctime := a.Env.Get("ctime")
		q.Set("lst", fmt.Sprintf("%s:%s:%s", b.Get("__user"), user.ID, ctime))
		q.Set("unit_cursor", cursor)
	}
	q.Set("v", "friends")

	sq := url.Values{}
	sq.Set("id", user.ID)
	sq.Set("lst", q.Get("lst"))
	sq.Set("v", "friends")
	referer := "/profile.php?" + sq.Encode()

	r := new(bytes.Buffer)
	encoder := json.NewEncoder(r)
	encoder.SetEscapeHTML(false)
	m := map[string]string{
		"s": "m",
		"r": referer,
		"h": referer,
	}
	encoder.Encode(&m)

	req.AddCookie(&http.Cookie{
		Name:   "x-referer",
		Value:  base64.RawStdEncoding.EncodeToString(r.Bytes()),
		Path:   "/",
		Domain: ".facebook.com",
	})

	req.URL.RawQuery = q.Encode()
	req.Header.Set("Referer", util.Host+referer)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Response-Format", "JSONStream")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err = a.do(req)

	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return nil, "", err
	}

	buf, err := handleResponseBasedErrors(resp)

	if err != nil {
		return nil, "", err
	}

	cb := util.ParseFromSource("unit_cursor=(.+?)&", buf)
	cursor = ""
	if cb != nil {
		a.Env.Increment()
		dtsgag := util.ParseFromSource("name=\\\\\"fb_dtsg\\\\\" value=\\\\\"([^\"]+)\\\\\"", buf)
		if dtsgag != nil {
			a.Env.Set("fb_dtsg", string(dtsgag))
		}

		cursor = string(cb)
	}
	links := parseAccountsLinks(buf)

	if links == nil {
		resp.Body.Close()
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(buf))
		rawReq, _ := httputil.DumpRequestOut(req, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		err = errors.NoFriendsError{
			Stage:    fmt.Sprintf("GetUserFriendsList (parsing friends for %s[%s])", user.Nickname, user.ID),
			Request:  rawReq,
			Response: rawResp,
		}
		resp.Body.Close()
		return nil, "", err
	}

	return composeAccounts(links), cursor, nil
}

func (a *FBAccount) GetIDFromNickname(nickname string) (string, error) {
	var req *http.Request
	var resp *http.Response
	var err error
	var q url.Values

	req = util.MakeRequest("GET", util.Host+"/"+nickname, nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	ctime := strconv.FormatInt(time.Now().Unix(), 10)
	a.Env.Set("ctime", ctime)
	q = a.Env.MakeBody([]string{
		"fb_dtsg_ag",
		"jazoest",
		"__dyn",
		"__req",
		"__ajax__",
		"__user",
	})

	q.Set("m_sess", "")
	q.Set("__disable_brotli__", "")
	q.Set("__big_pipe_on__", "")
	q.Set("__m_async_page__", "")
	q.Set("jazoest", jazoest([]byte(q.Get("fb_dtsg_ag"))))

	sq := url.Values{}

	sq.Set("lst", q.Get("lst"))
	sq.Set("v", "info")
	referer := "/profile.php?" + sq.Encode()

	r := new(bytes.Buffer)
	encoder := json.NewEncoder(r)
	encoder.SetEscapeHTML(false)
	m := map[string]string{
		"s": "m",
		"r": referer,
		"h": referer,
	}
	encoder.Encode(&m)

	req.AddCookie(&http.Cookie{
		Name:   "x-referer",
		Value:  base64.RawStdEncoding.EncodeToString(r.Bytes()),
		Path:   "/",
		Domain: ".facebook.com",
	})

	req.URL.RawQuery = q.Encode()
	req.Header.Set("Referer", util.Host+referer)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Response-Format", "JSONStream")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err = a.do(req)

	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return "", err
	}

	body, err := handleResponseBasedErrors(resp)

	if err != nil {
		return "", err
	}

	id := parseID(body)

	if id == "" {
		resp.Body.Close()
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(body))
		rawReq, _ := httputil.DumpRequestOut(req, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		err = errors.ParsingError{
			Stage:    fmt.Sprintf("GetIdFromNickname (%s)", nickname),
			Request:  rawReq,
			Response: rawResp,
		}
		resp.Body.Close()
		return "", err
	}

	return id, nil
}

func (a *FBAccount) GetAlbumPhotosIDS(user fb.Account, album string, offset int) ([]string, bool, error) {
	var req *http.Request
	var resp *http.Response
	var err error
	var q url.Values

	req = util.MakeRequest("GET", util.Host+"/media/set/", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	q = url.Values{}

	q.Set("set", "a."+album)
	q.Set("s", strconv.FormatInt(int64(offset*12), 10))
	q.Set("mode", "albumpermalink")

	referer := "/"

	if user.Nickname != "" {
		referer = referer + user.Nickname
	} else {
		referer = referer + user.ID
	}

	referer = referer + "/" + album + "/"

	r := new(bytes.Buffer)
	encoder := json.NewEncoder(r)
	encoder.SetEscapeHTML(false)
	m := map[string]string{
		"s": "m",
		"r": referer,
		"h": referer,
	}
	encoder.Encode(&m)

	req.AddCookie(&http.Cookie{
		Name:   "x-referer",
		Value:  base64.RawStdEncoding.EncodeToString(r.Bytes()),
		Path:   "/",
		Domain: ".facebook.com",
	})

	req.URL.RawQuery = q.Encode()
	req.Header.Set("Referer", util.Host+referer)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Response-Format", "JSONStream")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err = a.do(req)

	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return nil, false, err
	}

	buf, err := handleResponseBasedErrors(resp)

	if err != nil {
		return nil, false, err
	}

	links := parsePhotosLinks(buf, album)

	if links == nil {
		resp.Body.Close()
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(buf))
		rawReq, _ := httputil.DumpRequestOut(req, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		err = errors.ParsingError{
			Stage:    fmt.Sprintf("GetAlbumPhotosIDS (parsing photo ids from %s (%s[%s]))", album, user.Nickname, user.ID),
			Request:  rawReq,
			Response: rawResp,
		}
		resp.Body.Close()
		return nil, false, err
	}

	return links, strings.Contains(string(buf), "more-photos"), nil
}

func (a *FBAccount) GetPhotoFull(id string) (string, error) {
	var req *http.Request
	var resp *http.Response
	var err error
	var q url.Values

	req = util.MakeRequest("GET", util.Host+"/photo/view_full_size/", nil)

	q = url.Values{}

	q.Set("fbid", id)
	q.Set("ref_component", "mbasic_photo_permalink")
	q.Set("ref_page", "/wap/photo.php")

	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "*/*")

	resp, err = a.do(req)

	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return "", err
	}

	buf, err := handleResponseBasedErrors(resp)
	var l string = ""

	if err != nil {
		return l, err
	}

	link := parsePhotoFullLink(buf)

	if link == "" {
		resp.Body.Close()
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(buf))
		rawReq, _ := httputil.DumpRequestOut(req, true)
		rawResp, _ := httputil.DumpResponse(resp, true)
		err = errors.ParsingError{
			Stage:    fmt.Sprintf("GetPhotoFull (parsing full link for %s)", id),
			Request:  rawReq,
			Response: rawResp,
		}
		resp.Body.Close()
		return "", err
	}

	err = json.Unmarshal([]byte("\""+parsePhotoFullLink(buf)+"\""), &l)

	if err != nil {
		return "", err
	}

	if l != "" {
		return l, nil
	}
	return "", CouldNotParseLinkError{}
}

type CouldNotParseLinkError struct {
}

func (c CouldNotParseLinkError) Error() string {
	return "could not find link"
}

func parseAccountsLinks(source []byte) []string {
	re := regexp.MustCompile("darkTouch.*?href=\\\\\\\"\\\\\\/(.*?)refid")
	r := re.FindAllSubmatch(source, -1)
	var ret []string = nil
	if len(r) > 0 {
		ret = make([]string, len(r))
		for i, s := range r {
			ret[i] = string(s[1])
		}
	}
	return ret
}

func composeAccounts(links []string) []fb.Account {
	var ret []fb.Account = nil

	if l := len(links); l > 0 {
		ret = make([]fb.Account, l)
		for i, link := range links {
			if p := strings.Index(link, "id="); p > 0 {
				sp := p + 3
				lp := strings.Index(link, "&amp;")
				ret[i] = fb.Account{
					ID: link[sp:lp],
				}
			} else {
				ret[i] = fb.Account{
					Nickname: link[:len(link)-1],
				}
			}
		}
	}

	return ret
}

func parsePhotoFullLink(source []byte) string {
	re := regexp.MustCompile("document\\.location\\.href=\\\"(.*?)\\\"")
	r := re.FindSubmatch(source)
	if len(r) > 1 {
		return string(r[1])
	}
	return ""
}

func parsePhotosLinks(source []byte, album string) []string {
	re := regexp.MustCompile("\\\"\\/photo\\.php\\?fbid=([0-9]+).*?set=a\\." + album)
	r := re.FindAllSubmatch(source, -1)
	var ret []string = nil
	if r != nil {
		ret = make([]string, len(r))
		for i, l := range r {
			ret[i] = string(l[1])
		}
	}
	return ret
}

func parseID(source []byte) string {
	re := regexp.MustCompile("currentProfileID\\\\\":([0-9]+)}]")
	r := re.FindSubmatch(source)

	if r != nil {
		return string(r[1])
	}
	return ""
}

func parseUserAlbumIDs(source []byte) []string {
	re := regexp.MustCompile("touchable primary.*?albums\\\\\\/([0-9]+)\\\\\\/")
	f := re.FindAllSubmatch(source, -1)
	var ret []string = nil
	if len(f) > 0 {
		ret = make([]string, len(f))
		for i, s := range f {
			ret[i] = string(s[1])
		}
	}
	return ret
}

func parseFullName(source []byte) string {
	fnRe := regexp.MustCompile("\\\\\\\"setTitle\\\\\\\",\\[\\],\\[\\\\\\\"(.*?)\\\\\\\"\\]\\]")
	gr := fnRe.FindSubmatch(source)
	if len(gr) > 1 {
		str := string(gr[1])
		for strings.Contains(str, "\\") {
			json.Unmarshal([]byte("\""+str+"\""), &str)
		}
		return str
	} else {
		return ""
	}
}

func parseGender(source []byte) fb.Gender {
	fnRe := regexp.MustCompile("title=\\\\\\\"Gender\\\\\\\".*?>([A-Za-z]+?)\\\\u003C")
	gr := fnRe.FindSubmatch(source)
	if len(gr) > 1 {
		switch string(gr[1]) {
		case "Male":
			return fb.Male
		case "Female":
			return fb.Female
		default:
			return fb.Unknown
		}
	}
	return -1
}

func parsePlaces(source []byte) []string {
	fnRe := regexp.MustCompile("header id.*?h4>(.*?)\\\\u003C\\\\\\/h4>")
	gr := fnRe.FindAllSubmatch(source, -1)
	r := make([]string, len(gr))
	for i, c := range gr {
		r[i] = string(c[1])
	}
	return r
}

func parseCurrentCity(source []byte) string {
	fnRe := regexp.MustCompile("header id(?:.*)h4>([^\\\\]+)\\\\u003C\\\\\\/h4>(?:.*)Current City")
	gr := fnRe.FindSubmatch(source)
	if len(gr) > 1 {
		return string(gr[1])
	} else {
		return ""
	}
}

func parseHometown(source []byte) string {
	fnRe := regexp.MustCompile("header id(?:.*)h4>([^\\\\]+)\\\\u003C\\\\\\/h4>(?:.*)Hometown")
	gr := fnRe.FindSubmatch(source)
	if len(gr) > 1 {
		return string(gr[1])
	} else {
		return ""
	}
}

func parseNickName(source []byte) string {
	re := regexp.MustCompile("\\\"Facebook.*?\\/(.*?)\\\\u003C")
	r := re.FindSubmatch(source)

	if len(r) > 1 {
		return string(r[1])
	}

	return ""
}

var ResolvePlace func(string) *place.Place
