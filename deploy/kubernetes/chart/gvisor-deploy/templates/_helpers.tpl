{{- define "gvisor-deploy.name" -}}
{{- printf "%s-installer" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gvisor-deploy.labels" -}}
app.kubernetes.io/name: gvisor-deploy
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "gvisor-deploy.selector" -}}
app.kubernetes.io/name: gvisor-deploy
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "gvisor-deploy.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag }}
{{- end -}}
