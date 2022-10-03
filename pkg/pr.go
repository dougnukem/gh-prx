package pkg

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/AlecAivazis/survey/v2"
	"github.com/caarlos0/log"
	"github.com/pkg/errors"
	"github.com/samber/lo"
)

const (
	DefaultTitle = "{{.Type}}({{.Issue}}): {{ toSentence .Description}}"
	DefaultBody  = `Closes {{.Issue}}

## Description

{{ toSentence .Description}}

Change(s) in this PR:
{{range $commit := .Commits}}
* {{$commit}}
{{- end}}

## PR Checklist

- [ ] Tests are included
- [ ] Documentation is changed or added
`
)

var (
	DefaultIgnoreCommitsPattern = []string{`^wip`}
	TypeToLabel                 = map[string]string{
		"fix":  "bug",
		"feat": "enhancement",
		"docs": "documentation",
	}
	LabelToType = map[string]string{
		"bug":           "fix",
		"enhancement":   "feat",
		"documentation": "docs",
	}

	mdCheckboxMatcher = regexp.MustCompile(`^\s*[\-\*]\s*\[(x|\s)\]`)
)

type CommitsFetcher func() ([]string, error)

type PullRequestConfig struct {
	Title                 string   `yaml:"title"`
	IgnoreCommitsPatterns []string `yaml:"ignore_commits_patterns"`
	AnswerChecklist       *bool    `yaml:"answer_checklist"`

	Body string `yaml:"-"`
}

func (c *PullRequestConfig) SetDefaults() {
	if c.Title == "" {
		c.Title = DefaultTitle
	}

	if c.Body == "" {
		c.Body = DefaultBody
	}

	if len(c.IgnoreCommitsPatterns) == 0 {
		c.IgnoreCommitsPatterns = DefaultIgnoreCommitsPattern
	}

	if c.AnswerChecklist == nil {
		trueVal := true
		c.AnswerChecklist = &trueVal
	}
}

type PullRequest struct {
	Title  string
	Body   string
	Labels []string
}

func TemplatePR(
	b Branch,
	cfg PullRequestConfig,
	confirm bool,
	tokenSeparators []string,
	commitsFetcher CommitsFetcher,
) (PullRequest, error) {
	log.Debug("Templating PR")
	log.IncreasePadding()
	defer log.DecreasePadding()

	pr := PullRequest{}
	funcMaps := getTemplateFuncMaps(tokenSeparators)

	tpl := bytes.Buffer{}
	t, err := template.New("pr-title-tpl").Funcs(funcMaps).Parse(cfg.Title)
	if err != nil {
		return PullRequest{}, errors.Wrap(err, "Failed to parse pr title template")
	}
	if err := t.Option("missingkey=error").Execute(&tpl, b.Fields); err != nil {
		return PullRequest{}, errors.Wrap(err, "Failed to template pr title")
	}
	pr.Title = tpl.String()

	if strings.Contains(cfg.Body, ".Commits") {
		log.Debug("Fetching commits")
		commits, err := fetchCommits(cfg.IgnoreCommitsPatterns, commitsFetcher)
		if err != nil {
			return PullRequest{}, err
		}
		b.Fields["Commits"] = commits
	}

	tpl = bytes.Buffer{}
	t, err = template.New("pr-body-tpl").Funcs(funcMaps).Parse(cfg.Body)
	if err != nil {
		return PullRequest{}, errors.Wrap(err, "Failed to parse pr body template")
	}
	if err := t.Option("missingkey=error").Execute(&tpl, b.Fields); err != nil {
		return PullRequest{}, errors.Wrap(err, "Failed to template pr body")
	}
	pr.Body = tpl.String()

	if *cfg.AnswerChecklist {
		body, err := answerPRChecklist(pr.Body, confirm)
		if err != nil {
			return PullRequest{}, err
		}
		pr.Body = body
	}

	if typeAny, ok := b.Fields["Type"]; ok {
		if typeStr, ok := typeAny.(string); ok {
			label, ok := TypeToLabel[typeStr]
			if !ok {
				label = typeStr
			}

			pr.Labels = append(pr.Labels, label)
		}
	}

	return pr, nil
}

func getTemplateFuncMaps(tokenSeparators []string) template.FuncMap {
	return template.FuncMap{
		"toSentence": func(text string) (string, error) {
			tokenMatcher, err := regexp.Compile(fmt.Sprintf("[%s]", strings.Join(tokenSeparators, "")))
			if err != nil {
				return "", errors.Wrap(err, "Failed to compile toSentence token matcher")
			}

			return tokenMatcher.ReplaceAllString(text, " "), nil
		},
	}
}

func fetchCommits(ignoreCommitsPatterns []string, commitsFetcher CommitsFetcher) ([]string, error) {
	commits, err := commitsFetcher()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to fetch commits")
	}

	ignoreCommitsMatcher, err := regexp.Compile(strings.Join(ignoreCommitsPatterns, "|"))
	if err != nil {
		return nil, errors.Wrap(err, "Failed to compile ignore commits matcher")
	}

	commits = lo.Filter(commits, func(commit string, _ int) bool {
		return !ignoreCommitsMatcher.MatchString(commit)
	})

	return lo.Reverse(commits), nil
}

func answerPRChecklist(body string, confirm bool) (string, error) {
	if confirm {
		log.Info("Answering checklist (if exists in PR description)")
	} else {
		log.Info("Answering no for all checklist items (if exists in PR description)")
	}

	bodyLines := strings.Split(body, "\n")
	newBodyLines := []string{}
	for _, line := range bodyLines {
		addLine := true

		if mdCheckboxMatcher.MatchString(line) {
			q := mdCheckboxMatcher.ReplaceAllString(line, "")

			answer := "no"
			if !confirm {
				if err := survey.AskOne(&survey.Select{
					Message: q,
					Options: []string{"yes", "no", "skip"},
				}, &answer, survey.WithValidator(survey.Required)); err != nil {
					return "", errors.Wrap(err, "Failed to ask for action item status")
				}
			}

			switch answer {
			case "yes":
				line = mdCheckboxMatcher.ReplaceAllString(line, "- [x]")
			case "no":
				line = mdCheckboxMatcher.ReplaceAllString(line, "- [ ]")
			case "skip":
				addLine = false
			}
		}

		if addLine {
			newBodyLines = append(newBodyLines, line)
		}
	}

	return strings.Join(newBodyLines, "\n"), nil
}