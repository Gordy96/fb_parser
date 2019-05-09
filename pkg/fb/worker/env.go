package worker

import (
	"bytes"
	"encoding/json"
	"fbParser/pkg/fb/util"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

type Env struct {
	Cookies		[]*http.Cookie			`bson:"cookies"`
	Variables	map[string]string		`bson:"variables"`
	mux			sync.Mutex				`bson:"-"`
}

func (e Env) String() string {
	var b = make([]byte, 10240)
	wr := bytes.NewBuffer(b)
	encoder := json.NewEncoder(wr)
	encoder.SetIndent("", "\t")
	encoder.Encode(e)
	return wr.String()
}

func (e Env) MakeBody(fields []string) url.Values {
	p := url.Values{}
	for _, k := range fields {
		if v, b := e.Variables[k]; b {
			p.Set(k, v)
		}
	}
	return p
}

func (e Env) Get(key string) string {
	return e.Variables[key]
}

func (e *Env) Set(key string, value string) {
	e.Variables[key] = value
}

func (e *Env) Increment() {
	e.mux.Lock()
	i, _ := strconv.ParseInt(e.Variables["__req"], 36, 64)
	i++
	e.Variables["__req"] = strconv.FormatInt(i, 36)
	e.mux.Unlock()
}

func jazoest(b []byte) string {
	var tmp = 0
	for _, c := range b {
		tmp += int(c)
	}
	return "2" + strconv.Itoa(tmp)
}

func MakeEnv(resp *http.Response, userstr string) Env {
	source := util.ReadAll(resp)
	m := make(map[string]string)

	m["li"] = string(util.GrepFormValue("li", source))
	m["m_ts"] = string(util.GrepFormValue("m_ts", source))
	m["try_number"] = string(util.GrepFormValue("try_number", source))
	m["unrecognized_tries"] = string(util.GrepFormValue("unrecognized_tries", source))

	parts := strings.Split(userstr, ":")

	m["email"] = parts[0]
	m["pass"] = parts[1]

	m["prefill_contact_point"] = m["email"]
	m["prefill_source"] = "browser_dropdown"
	m["prefill_type"] = "password"
	m["first_prefill_source"] = m["prefill_source"]
	m["first_prefill_type"] = "contact_point"
	m["had_cp_prefilled"] = "true"
	m["had_password_prefilled"] = "true"
	m["is_smart_lock"] = "false"
	m["m_sess"] = ""

	dtsg := util.GetDTSG(source)

	m["fb_dtsg"] = string(dtsg)
	m["fb_dtsg_ag"] = string(util.GetDTSGAG(source))

	m["jazoest"] = jazoest(dtsg)

	m["lsd"] = string(util.GrepFormValue("lsd", source))
	m["__req"] = "1"
	m["__ajax__"] = string(util.GetAJAX(source))
	m["__user"] = "0"
	m["__dyn"] = util.Dyn

	e := Env{}
	e.mux = sync.Mutex{}

	c := []*http.Cookie{
		util.GetFRCookie(source, resp),
		util.GetSBCookie(source, resp),
		util.GetDATRCookie(source, resp),
	}
	e.Cookies = c
	e.Variables = m

	return e
}
