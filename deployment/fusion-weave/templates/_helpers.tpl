{{/*
SPDX-License-Identifier: GPL-3.0-or-later
Common labels applied to every resource.
*/}}
{{- define "fusion-weave.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels used by the Deployment and Service.
*/}}
{{- define "fusion-weave.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Full image reference for the operator.
*/}}
{{- define "fusion-weave.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag }}
{{- end }}

{{/*
Full image reference for the API server.
Falls back to the operator image when api.image.repository is not set.
*/}}
{{- define "fusion-weave.api.image" -}}
{{- $repo := default .Values.image.repository .Values.api.image.repository -}}
{{- $tag  := default .Values.image.tag .Values.api.image.tag -}}
{{ $repo }}:{{ $tag }}
{{- end }}

{{/*
Selector labels for the API server Deployment / Service.
*/}}
{{- define "fusion-weave.api.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}-api
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
