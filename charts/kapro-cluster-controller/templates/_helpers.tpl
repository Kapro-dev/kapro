{{/*
Expand the name of the chart.
*/}}
{{- define "kapro-cluster-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kapro-cluster-controller.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "kapro-cluster-controller.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "kapro-cluster-controller.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{ include "kapro-cluster-controller.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: spoke-agent
app.kubernetes.io/part-of: kapro
{{- end }}

{{- define "kapro-cluster-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kapro-cluster-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kapro-cluster-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kapro-cluster-controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
