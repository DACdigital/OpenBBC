{{/*
Expand the name of the chart.
*/}}
{{- define "openbbc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified app name (release-name + chart-name), overridable.
*/}}
{{- define "openbbc.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "openbbc.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels for every object.
*/}}
{{- define "openbbc.labels" -}}
helm.sh/chart: {{ include "openbbc.chart" . }}
app.kubernetes.io/name: {{ include "openbbc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Component-scoped selector labels.
Usage: {{ include "openbbc.selectorLabels" (dict "root" . "component" "openbbcd") }}
*/}}
{{- define "openbbc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "openbbc.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "openbbc.openbbcd.fullname" -}}
{{- printf "%s-openbbcd" (include "openbbc.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "openbbc.postgres.fullname" -}}
{{- printf "%s-postgres" (include "openbbc.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "openbbc.aikdmRunner.fullname" -}}
{{- printf "%s-aikdm-runner" (include "openbbc.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Image references — appVersion is used as the tag when values.tag is empty.
*/}}
{{- define "openbbc.openbbcd.image" -}}
{{- $repo := .Values.openbbcd.image.repository -}}
{{- $tag  := default .Chart.AppVersion .Values.openbbcd.image.tag -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}

{{- define "openbbc.aikdmRunner.image" -}}
{{- $repo := .Values.aikdmRunner.image.repository -}}
{{- $tag  := default .Chart.AppVersion .Values.aikdmRunner.image.tag -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}

{{/*
Name of the Secret carrying DATABASE_URL for open-bbcd.
When externalDatabase.existingSecret is set, use it verbatim; otherwise this
chart renders a Secret named "<fullname>-db".
*/}}
{{- define "openbbc.databaseSecretName" -}}
{{- if and (not .Values.postgres.enabled) .Values.externalDatabase.existingSecret -}}
{{- .Values.externalDatabase.existingSecret -}}
{{- else -}}
{{- printf "%s-db" (include "openbbc.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "openbbc.databaseSecretKey" -}}
{{- if and (not .Values.postgres.enabled) .Values.externalDatabase.existingSecret -}}
{{- .Values.externalDatabase.existingSecretKey -}}
{{- else -}}
DATABASE_URL
{{- end -}}
{{- end -}}

{{/*
Name of the Secret carrying LLM API keys for aikdm cron pods.
*/}}
{{- define "openbbc.aikdmSecretName" -}}
{{- if .Values.aikdmRunner.secrets.existingSecret -}}
{{- .Values.aikdmRunner.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-aikdm" (include "openbbc.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Postgres password secret name (only when postgres.enabled=true).
*/}}
{{- define "openbbc.postgresSecretName" -}}
{{- if .Values.postgres.auth.existingSecret -}}
{{- .Values.postgres.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-postgres" (include "openbbc.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Cron pods talk to open-bbcd via HTTP. Default to the in-cluster Service.
*/}}
{{- define "openbbc.openbbcdUrl" -}}
{{- if .Values.aikdmRunner.openbbcdUrlOverride -}}
{{- .Values.aikdmRunner.openbbcdUrlOverride -}}
{{- else -}}
{{- printf "http://%s:%d" (include "openbbc.openbbcd.fullname" .) (int .Values.openbbcd.service.port) -}}
{{- end -}}
{{- end -}}

{{/*
Postgres password resolution — used by the postgres StatefulSet's own Secret
and by the DATABASE_URL Secret. Precedence:
  1. postgres.auth.password (explicit) — wins, reproducible with helm template
  2. Existing in-cluster Secret at <fullname>-postgres (upgrade-safe)
  3. Freshly generated randAlphaNum(24) — first install only
Note: (2) requires cluster access, so `helm template` without --dry-run against
a cluster will fall through to (3) and produce a new password each render. For
reproducible dry-runs, pin postgres.auth.password.
*/}}
{{- define "openbbc.postgresPassword" -}}
{{- if .Values.postgres.auth.password -}}
{{- .Values.postgres.auth.password -}}
{{- else -}}
{{- $secretName := printf "%s-postgres" (include "openbbc.fullname" .) -}}
{{- $existing := (lookup "v1" "Secret" .Release.Namespace $secretName) -}}
{{- if and $existing (index $existing.data "password") -}}
{{- index $existing.data "password" | b64dec -}}
{{- else -}}
{{- randAlphaNum 24 -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
DATABASE_URL string built from in-cluster Postgres creds.
*/}}
{{- define "openbbc.databaseUrl" -}}
{{- $pw := include "openbbc.postgresPassword" . -}}
{{- printf "postgres://%s:%s@%s:%d/%s?sslmode=%s"
    .Values.postgres.auth.username
    $pw
    (include "openbbc.postgres.fullname" .)
    (int .Values.postgres.service.port)
    .Values.postgres.auth.database
    .Values.postgres.sslmode -}}
{{- end -}}
