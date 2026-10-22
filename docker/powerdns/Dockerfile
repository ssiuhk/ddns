FROM powerdns/pdns-auth-51:5.1.4

# The official image renders every template listed in TEMPLATE_FILES from
# /etc/powerdns/templates.d into /etc/powerdns/pdns.d at startup, using the
# environment of the container (here PDNS_REMOTE_HTTP_HOST).
COPY pdns.conf /etc/powerdns/pdns.conf
COPY ddns.j2 /etc/powerdns/templates.d/ddns.j2

ENV TEMPLATE_FILES=ddns
