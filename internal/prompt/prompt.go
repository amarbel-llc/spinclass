package prompt

import (
	"bytes"
	"embed"
	"text/template"
)

//go:embed system_prompt_append.d/*.tmpl
var templates embed.FS

type BaseData struct {
	RepoName   string
	RemoteURL  string
	Branch     string
	SessionID  string
	IsFork     bool
	OwnerType  string
	OwnerLogin string
}

type IssueData struct {
	Number int
	Title  string
	State  string
	Labels string
	URL    string
	Body   string
}

type PRData struct {
	Number  int
	Title   string
	State   string
	BaseRef string
	HeadRef string
	Labels  string
	URL     string
	Body    string
}

func render(name string, data any) (string, error) {
	tmpl, err := template.ParseFS(templates, "system_prompt_append.d/"+name)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func RenderBase(data BaseData) (string, error) {
	return render("0-base.md.tmpl", data)
}

func RenderIssue(data IssueData) (string, error) {
	return render("1-issue.md.tmpl", data)
}

func RenderPR(data PRData) (string, error) {
	return render("1-pr.md.tmpl", data)
}
