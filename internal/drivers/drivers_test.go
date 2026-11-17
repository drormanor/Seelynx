package drivers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"seelynx/internal/ai"
)

const crossed = "tns1:RuleEngine/LineDetector/Crossed"
const loiter = "tns1:VideoAnalytics/tnsht:LoiteringDetector/ObjectIsLoitering"

var report = Report{
	Manufacturer: "Vicon ", Model: "V2002D-W28IR", Firmware: "1.0",
	Topics: []Topic{{Topic: crossed, Fields: []string{"Value"}}, {Topic: loiter, Fields: []string{"Value"}}},
}

func TestAnswerOnlyDescribesWhatTheCameraReported(t *testing.T) {
	d, err := FromAnswer(report, `{"analytics":[
		{"topic":"`+crossed+`","name":"Line Cross","description":"Someone crosses a line."},
		{"topic":"tns1:Invented/Topic","name":"Fake","description":"Not real."}
	],"notes":"Mount it high."}`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Manufacturer != "Vicon" || d.Model != "V2002D-W28IR" || d.Notes != "Mount it high." {
		t.Fatalf("%+v", d)
	}
	if len(d.Analytics) != 2 || d.Analytics[0].Name != "Line Cross" {
		t.Fatalf("%+v", d.Analytics)
	}
	// The AI skipped this one, so it gets a plain name rather than vanishing.
	if d.Analytics[1].Topic != loiter || d.Analytics[1].Name != "ObjectIsLoitering" {
		t.Fatalf("%+v", d.Analytics[1])
	}
	if msg := d.Validate(); msg != "" {
		t.Fatal(msg)
	}
}

func TestAnswerWrappedInTextIsRead(t *testing.T) {
	d, err := FromAnswer(report, "Here you go:\n```json\n{\"analytics\":[{\"topic\":\""+crossed+"\",\"name\":\"Line Cross\",\"description\":\"\"}],\"notes\":\"\"}\n```")
	if err != nil || d.Analytics[0].Name != "Line Cross" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestBadAnswersAreRefused(t *testing.T) {
	for _, answer := range []string{"", "sorry", "{not json}"} {
		if _, err := FromAnswer(report, answer); err == nil {
			t.Errorf("%q accepted", answer)
		}
	}
}

func TestAnswerTextIsTidied(t *testing.T) {
	d, err := FromAnswer(report, `{"analytics":[{"topic":"`+crossed+`","name":"Line\nCross\u0000","description":"`+strings.Repeat("x", 500)+`"}],"notes":""}`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Analytics[0].Name != "Line Cross" || len(d.Analytics[0].Description) != maxText {
		t.Fatalf("%q %d", d.Analytics[0].Name, len(d.Analytics[0].Description))
	}
}

func TestReportCheck(t *testing.T) {
	good := report
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	noModel := report
	noModel.Model = " "
	noTopics := report
	noTopics.Topics = nil
	tooMany := report
	tooMany.Topics = make([]Topic, maxTopics+1)
	for i := range tooMany.Topics {
		tooMany.Topics[i].Topic = "t"
	}
	for _, r := range []Report{noModel, noTopics, tooMany} {
		if r.Check() == nil {
			t.Errorf("%+v accepted", r)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Analytic{Topic: "a", Name: "A"}
	bad := []Driver{
		{Model: "m", Analytics: []Analytic{ok}},
		{Manufacturer: "x", Analytics: []Analytic{ok}},
		{Manufacturer: "x", Model: "m"},
		{Manufacturer: "x", Model: "m", Analytics: []Analytic{ok, ok}},
		{Manufacturer: "x", Model: "m", Analytics: []Analytic{{Topic: "a", Name: " "}}},
		{Manufacturer: "x", Model: "m", Analytics: []Analytic{{Topic: "a", Name: strings.Repeat("n", maxName+1)}}},
	}
	for _, d := range bad {
		if d.Validate() == "" {
			t.Errorf("%+v accepted", d)
		}
	}
}

func TestFindAndName(t *testing.T) {
	list := []Driver{{Manufacturer: "Vicon", Model: "V2002D", Analytics: []Analytic{{Topic: crossed, Name: "Line Cross"}}}}
	d, ok := Find(list, " vicon", "v2002d ")
	if !ok {
		t.Fatal("not found")
	}
	if n, ok := d.Name(crossed); !ok || n != "Line Cross" {
		t.Fatal(n)
	}
	if _, ok := d.Name("other"); ok {
		t.Fatal("unknown topic named")
	}
	if _, ok := Find(list, "Vicon", "Other"); ok {
		t.Fatal("wrong model matched")
	}
}

func TestBuildSendsOnlyTheReport(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sent = string(raw)
		answer, _ := json.Marshal(`{"analytics":[{"topic":"` + crossed + `","name":"Line Cross","description":"d"}],"notes":""}`)
		io.WriteString(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":`+string(answer)+`}]}`)
	}))
	defer srv.Close()
	d, err := Build(context.Background(), ai.Config{Provider: ai.Anthropic, BaseURL: srv.URL, APIKey: "k"}, report)
	if err != nil {
		t.Fatal(err)
	}
	if d.Analytics[0].Name != "Line Cross" {
		t.Fatalf("%+v", d)
	}
	if !strings.Contains(sent, "V2002D-W28IR") || !strings.Contains(sent, "LineDetector") {
		t.Fatal(sent)
	}
}
