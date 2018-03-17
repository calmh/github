package github

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/russross/blackfriday"
)

type Issue struct {
	ID          int
	URL         string
	HTMLURL     string `json:"html_url"`
	Number      int
	State       string
	Title       string
	Body        string
	User        User
	Labels      []Label
	Assignee    User
	Milestone   Milestone
	PullRequest struct {
		URL string
	} `json:"pull_request"`
	Closed  *time.Time `json:"closed_at"` // nil for open issues
	Created time.Time  `json:"created_at"`
	Updated time.Time  `json:"updated_at"`
}

func (i Issue) BodyHTML() template.HTML {
	unsafe := blackfriday.MarkdownCommon([]byte(i.Body))
	return template.HTML(bluemonday.UGCPolicy().SanitizeBytes(unsafe))
}

func (i Issue) Type() string {
	if i.PullRequest.URL != "" {
		return "PR"
	}
	return "Issue"
}

type Milestone struct {
	URL         string
	HTMLURL     string `json:"html_url"`
	ID          int
	Number      int
	State       string
	Title       string
	Description string
	Creator     User
	Due         *time.Time `json:"due_on"`
	Closed      *time.Time `json:"closed_at"` // nil for open milestones
	Created     time.Time  `json:"created_at"`
	Updated     time.Time  `json:"updated_at"`
}

type User struct {
	Login string
	ID    int
	Email string
}

type Label struct {
	Name  string
	Color string
}

type Release struct {
	ID         int
	TagName    string `json:"tag_name"`
	Name       string
	Body       string
	Draft      bool
	Prerelease bool
	Created    time.Time `json:"created_at"`
	Published  time.Time `json:"published_at"`
	Author     User
	Assets     []Asset
}

type Asset struct {
	BrowserDownloadURL string `json:"browser_download_url"`
	ID                 int
	Name               string
	Label              string
	State              string
	ContentType        string `json:"content_type"`
	Size               int
	DownloadCount      int       `json:"download_count"`
	Created            time.Time `json:"created_at"`
	Updated            time.Time `json:"updated_at"`
	Uploader           User
}

type Team struct {
	Name string
	ID   int
	Slug string
	URL  string
}

type Notification struct {
	ID         json.Number
	Repository struct {
		Name string `json:"full_name"`
	}
	Subject struct {
		Title string
		Type  string
		URL   string
	}
	Reason string
	Unread bool
}

func LoadIssues(repo string, query url.Values) ([]Issue, error) {
	link := "https://" + path.Join("api.github.com/repos", repo, "issues")
	if query != nil {
		link += "?" + query.Encode()
	}
	issues, err := loadSlice(link, Issue{})
	if err != nil {
		return nil, err
	}
	return issues.([]Issue), nil
}

func LoadMilestones(repo string, query url.Values) ([]Milestone, error) {
	link := "https://" + path.Join("api.github.com/repos", repo, "milestones")
	if query != nil {
		link += "?" + query.Encode()
	}
	issues, err := loadSlice(link, Milestone{})
	if err != nil {
		return nil, err
	}
	return issues.([]Milestone), nil
}

func LoadReleases(repo string) ([]Release, error) {
	link := "https://" + path.Join("api.github.com/repos", repo, "releases")
	rels, err := loadSlice(link, Release{})
	if err != nil {
		return nil, err
	}
	return rels.([]Release), nil
}

func LoadTeams(org string) ([]Team, error) {
	link := "https://" + path.Join("api.github.com/orgs", org, "teams")
	rels, err := loadSlice(link, Team{})
	if err != nil {
		return nil, err
	}
	return rels.([]Team), nil
}

func LoadTeamMembers(teamID int) ([]User, error) {
	link := "https://" + path.Join("api.github.com/teams", strconv.Itoa(teamID), "members")
	rels, err := loadSlice(link, User{})
	if err != nil {
		return nil, err
	}
	return rels.([]User), nil
}

func LoadNotifications() ([]Notification, error) {
	link := "https://" + path.Join("api.github.com/notifications")
	rels, err := loadSlice(link, Notification{})
	if err != nil {
		return nil, err
	}
	return rels.([]Notification), nil
}

func GetUserEmail(username string) (string, error) {
	link := "https://" + path.Join("api.github.com/users", username)
	var user User
	if err := requestInto(link, &user); err != nil {
		return "", err
	}
	return user.Email, nil
}

func requestInto(link string, v interface{}) error {
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return err
	}

	setAuthentication(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		lr := io.LimitReader(resp.Body, 1024)
		bs, _ := ioutil.ReadAll(lr)
		return fmt.Errorf("http.Get: %v (%s)", resp.Status, bs)
	}

	return json.NewDecoder(resp.Body).Decode(v)
}

// loadSlice loads url and decodes it into a []elemType, returning the []elemType and error.
func loadSlice(url string, elemType interface{}) (interface{}, error) {
	t := reflect.TypeOf(elemType)
	result := reflect.New(reflect.SliceOf(t)).Elem() // result is []elemType

	link := url
	for link != "" {
		req, err := http.NewRequest("GET", link, nil)
		if err != nil {
			return result.Interface(), err
		}

		setAuthentication(req)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return result.Interface(), err
		}
		if resp.StatusCode > 299 {
			lr := io.LimitReader(resp.Body, 1024)
			bs, _ := ioutil.ReadAll(lr)
			resp.Body.Close()
			return result.Interface(), fmt.Errorf("http.Get: %v (%s)", resp.Status, bs)
		}

		tmp := reflect.New(reflect.SliceOf(t)) // tmp is *[]elemType
		err = json.NewDecoder(resp.Body).Decode(tmp.Interface())
		resp.Body.Close()
		if err != nil {
			return result.Interface(), err
		}

		result = reflect.AppendSlice(result, tmp.Elem())
		link = parseRel(resp.Header.Get("Link"), "next")
	}

	return result.Interface(), nil
}

func parseRel(link, rel string) string {
	exp := regexp.MustCompile(`<([^>]+)>;\s+rel="` + rel + `"`)
	match := exp.FindStringSubmatch(link)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func setAuthentication(req *http.Request) {
	username := os.Getenv("GITHUB_USERNAME")
	token := os.Getenv("GITHUB_TOKEN")
	if username != "" && token != "" {
		req.SetBasicAuth(username, token)
	}
}
