## Changes
{{ printf "\n" -}}
{{ range $section := .Sections -}}
{{ if eq $section.SectionType "commits" -}}
{{ range $commit := $section.Items -}}
- {{ if $commit.Message.Type }}{{ $commit.Message.Type }}{{ if $commit.Message.Scope }}({{ $commit.Message.Scope }}){{ end }}{{ if $commit.Message.IsBreakingChange }}!{{ end }}: {{ end }}{{ $commit.Message.Description }} ([{{ $commit.Hash }}]({{ env "GITHUB_SERVER_URL" }}/{{ env "GITHUB_REPOSITORY" }}/commit/{{ $commit.Hash }}))
{{ end -}}
{{ end -}}
{{ end -}}
