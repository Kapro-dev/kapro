{{/*
Expand the name of the chart.
*/}}
{{- define "kapro-archiver.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kapro-archiver.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "kapro-archiver.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "kapro-archiver.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{ include "kapro-archiver.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: event-archive
app.kubernetes.io/part-of: kapro
{{- end }}

{{- define "kapro-archiver.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kapro-archiver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kapro-archiver.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kapro-archiver.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
