package haproxy_test

import (
	"code.cloudfoundry.org/cf-tcp-router/config"
	"code.cloudfoundry.org/cf-tcp-router/configurer/haproxy"
	"code.cloudfoundry.org/cf-tcp-router/models"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("ConfigMarshaller", func() {
	Describe("Marshal", func() {
		var (
			haproxyConf   models.HAProxyConfig
			marshaller    haproxy.ConfigMarshaller
			logger        lager.Logger
			backendTlsCfg config.BackendTLSConfig
		)

		BeforeEach(func() {
			logger = lagertest.NewTestLogger("config-marshaller-test")
			haproxyConf = models.HAProxyConfig{}
			marshaller = haproxy.NewConfigMarshaller(logger)
			backendTlsCfg = config.BackendTLSConfig{
				CACertificatePath: "/fake/path/to/ca.pem",
			}
		})

		Context("when there is only a non-SNI route", func() {
			It("includes only the `default_backend` directive", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"": {{Address: "default-host.internal", Port: 8080}},
					},
				}

				Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  default_backend backend_80

backend backend_80
  mode tcp
  server server_default-host.internal_8080 default-host.internal:8080
`))
			})
		})

		Context("when there is only an SNI route", func() {
			It("includes only the SNI `use_backend` directive", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"external-host.example.com": {{Address: "default-host.internal", Port: 8080}},
					},
				}

				Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  tcp-request inspect-delay 5s
  tcp-request content accept if { req.ssl_hello_type gt 0 }
  use_backend backend_80_external-host.example.com if { req.ssl_sni external-host.example.com }

backend backend_80_external-host.example.com
  mode tcp
  server server_default-host.internal_8080 default-host.internal:8080
`))
			})
		})

		Context("when there is both an SNI route and a non-SNI route", func() {
			It("includes both types of directives", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"":                          {{Address: "default-host.internal", Port: 8080}},
						"external-host.example.com": {{Address: "sni-host.internal", Port: 9090}},
					},
				}
				actual := marshaller.Marshal(haproxyConf, backendTlsCfg)
				Expect(actual).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  tcp-request inspect-delay 5s
  tcp-request content accept if { req.ssl_hello_type gt 0 }
  default_backend backend_80
  use_backend backend_80_external-host.example.com if { req.ssl_sni external-host.example.com }

backend backend_80
  mode tcp
  server server_default-host.internal_8080 default-host.internal:8080

backend backend_80_external-host.example.com
  mode tcp
  server server_sni-host.internal_9090 sni-host.internal:9090
`))
			})
		})

		Context("when there are multiple inbound ports", func() {
			It("sorts the inbound ports", func() {
				haproxyConf = models.HAProxyConfig{
					90: {
						"": {{Address: "host-90.internal", Port: 9090}},
					},
					70: {
						"": {{Address: "host-70.internal", Port: 7070}},
					},
					80: {
						"": {{Address: "host-80.internal", Port: 8080}},
					},
				}
				Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_70
  mode tcp
  bind :70
  default_backend backend_70

backend backend_70
  mode tcp
  server server_host-70.internal_7070 host-70.internal:7070

frontend frontend_80
  mode tcp
  bind :80
  default_backend backend_80

backend backend_80
  mode tcp
  server server_host-80.internal_8080 host-80.internal:8080

frontend frontend_90
  mode tcp
  bind :90
  default_backend backend_90

backend backend_90
  mode tcp
  server server_host-90.internal_9090 host-90.internal:9090
`))
			})
		})

		Context("when there are multiple SNI hostnames for an inbound port", func() {
			It("sorts the SNI hostnames", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"host-99.example.com": {{Address: "host-99.internal", Port: 9999}},
						"":                    {{Address: "default-host.internal", Port: 8080}},
						"host-1.example.com":  {{Address: "host-1.internal", Port: 1111}},
					},
				}

				Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  tcp-request inspect-delay 5s
  tcp-request content accept if { req.ssl_hello_type gt 0 }
  default_backend backend_80
  use_backend backend_80_host-1.example.com if { req.ssl_sni host-1.example.com }
  use_backend backend_80_host-99.example.com if { req.ssl_sni host-99.example.com }

backend backend_80
  mode tcp
  server server_default-host.internal_8080 default-host.internal:8080

backend backend_80_host-1.example.com
  mode tcp
  server server_host-1.internal_1111 host-1.internal:1111

backend backend_80_host-99.example.com
  mode tcp
  server server_host-99.internal_9999 host-99.internal:9999
`))
			})
		})

		Context("when there are multiple servers for a backend", func() {
			It("retains the original order of the servers", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"": {
							{Address: "host-88.internal", Port: 8888},
							{Address: "host-99.internal", Port: 9999},
							{Address: "host-77.internal", Port: 7777},
						},
					},
				}
				Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  default_backend backend_80

backend backend_80
  mode tcp
  server server_host-88.internal_8888 host-88.internal:8888
  server server_host-99.internal_9999 host-99.internal:9999
  server server_host-77.internal_7777 host-77.internal:7777
`))
			})
		})

		Context("when a TLSPort and CA cert filepath are provided", func() {
			It("configures the backend server to use the TLSPort", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"": {
							{Address: "host-88.internal", Port: 8888, TLSPort: 8443, InstanceID: "host-88-instance-id"},
						},
					},
				}
				Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  default_backend backend_80

backend backend_80
  mode tcp
  server server_host-88.internal_8443 host-88.internal:8443 ssl verify required verifyhost host-88-instance-id ca-file /fake/path/to/ca.pem
`))
			})

			Context("when a client cert is provided", func() {
				BeforeEach(func() {
					backendTlsCfg.ClientCertAndKeyPath = "/fake/path/to/client_cert_and_key.pem"
				})
				It("configures the backend server to use the TLSPort with mTLS", func() {
					haproxyConf = models.HAProxyConfig{
						80: {
							"": {
								{Address: "host-88.internal", Port: 8888, TLSPort: 8443, InstanceID: "host-88-instance-id"},
							},
						},
					}
					Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  default_backend backend_80

backend backend_80
  mode tcp
  server server_host-88.internal_8443 host-88.internal:8443 ssl verify required verifyhost host-88-instance-id ca-file /fake/path/to/ca.pem crt /fake/path/to/client_cert_and_key.pem
`))

				})
			})
		})

		Context("when a TLSPort is provided but no CA cert filepath is provided", func() {
			It("configures the backend server to use the TLSPort", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"": {
							{Address: "host-88.internal", Port: 8888, TLSPort: 8443, InstanceID: "host-88-instance-id"},
						},
					},
				}
				Expect(marshaller.Marshal(haproxyConf, config.BackendTLSConfig{})).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  default_backend backend_80

backend backend_80
  mode tcp
`))
				Expect(logger).To(gbytes.Say("Backend TLS Port was set, but CA file path has not been configured"))
			})
		})

		Context("when TLSPort is 0", func() {
			It("Logs an error indicating that the backend is not being encrypted", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"": {
							{Address: "host-88.internal", Port: 8888, TLSPort: 0, InstanceID: "host-88-instance-id"},
						},
					},
				}
				marshaller.Marshal(haproxyConf, backendTlsCfg)
				Expect(logger).To(gbytes.Say("route-missing-tls-information"))
			})
			It("uses the non-tls backend port", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"": {
							{Address: "host-88.internal", Port: 8888, TLSPort: -1, InstanceID: "host-88-instance-id"},
						},
					},
				}
				Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  default_backend backend_80

backend backend_80
  mode tcp
  server server_host-88.internal_8888 host-88.internal:8888
`))
			})
		})
		Context("when TLSPort is -1", func() {
			It("does not log an error", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"": {
							{Address: "host-88.internal", Port: 8888, TLSPort: -1, InstanceID: "host-88-instance-id"},
						},
					},
				}
				marshaller.Marshal(haproxyConf, backendTlsCfg)
				Expect(logger).NotTo(gbytes.Say("route-missing-tls-information"))
			})
			It("uses the non-tls backend port", func() {
				haproxyConf = models.HAProxyConfig{
					80: {
						"": {
							{Address: "host-88.internal", Port: 8888, TLSPort: -1, InstanceID: "host-88-instance-id"},
						},
					},
				}
				Expect(marshaller.Marshal(haproxyConf, backendTlsCfg)).To(Equal(`
frontend frontend_80
  mode tcp
  bind :80
  default_backend backend_80

backend backend_80
  mode tcp
  server server_host-88.internal_8888 host-88.internal:8888
`))

			})
		})
	})
})
