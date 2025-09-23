{{/*
Expand the name of the chart.
*/}}
{{- define "app.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 59 | trimSuffix "-" }}{{ "app" }}
{{- end }}
{{- define "web.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 59 | trimSuffix "-" }}{{ "web" }}
{{- end }}
{{- define "worker.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 59 | trimSuffix "-" }}{{ "-worker" }}
{{- end }}


{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "app.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 59 | trimSuffix "-" }}{{ "app" }}
{{- end }}
{{- define "web.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 59| trimSuffix "-" }}{{ "web" }}
{{- end }}
{{- define "worker.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 59 | trimSuffix "-" }}{{ "-worker" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "app.labels" -}}
helm.sh/chart: {{ include "app.chart" . }}
{{ include "app.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "web.labels" -}}
helm.sh/chart: {{ include "web.chart" . }}
{{ include "web.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "worker.labels" -}}
helm.sh/chart: {{ include "worker.chart" . }}
{{ include "worker.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "web.selectorLabels" -}}
app.kubernetes.io/name: {{ include "web.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "worker.selectorLabels" -}}
app.kubernetes.io/name: {{ include "worker.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "app.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "app.name" .) .Values.serviceAccount.name }}{{ "app" }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}{{ "app" }}
{{- end }}
{{- end }}
{{- define "web.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "web.name" .) .Values.serviceAccount.name }}{{ "web" }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}{{ "web" }}
{{- end }}
{{- end }}
{{- define "worker.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "worker.name" .) .Values.serviceAccount.name }}{{ "-worker" }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}{{ "-worker" }}
{{- end }}
{{- end }}

{{/*
Create the name of the secret. Default to the service name, allow overriding.
*/}}
{{- define "app.secretName" -}}
{{- default (include "app.name" .) .Values.secretName }}
{{- end }}
{{- define "web.secretName" -}}
{{- default (include "web.name" .) .Values.secretName }}
{{- end }}

{{/*
Create migrate job name. For hosted environments, default to the app version.
This prevents problems with the job's image being immutable.
*/}}
{{- define "app.migrationJobName" -}}
{{- if .Values.migrations.setJobNameAsTimestamp }}
{{- printf "%s-migrate-%s" .Values.migrations.jobName (now | date "20060102150405") }}
{{- else }}
{{- printf "%s-migrate-%s" .Values.migrations.jobName .Chart.AppVersion }}
{{- end }}
{{- end }}
