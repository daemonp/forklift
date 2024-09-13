# Traefik Forklift A/B Testing Middleware

This Traefik middleware plugin enables advanced A/B testing and traffic routing capabilities for your Docker and Kubernetes environments. It allows you to route traffic to different backend services based on various conditions, including request path, method, query parameters, form data, headers, and more.

## Features

-   Dynamic routing based on multiple conditions
-   Support for gradual rollout with percentage-based routing
-   Session affinity for consistent user experience
-   Path prefix rewriting
-   Flexible configuration using Traefik's middleware plugin system
-   Compatible with Docker Compose and Kubernetes ingress

## Prerequisites

-   Go 1.16 or later
-   Traefik v2.5 or later
-   Docker and Docker Compose (for local testing)
-   Kubernetes cluster (for Kubernetes deployment)

## Building the Plugin

1. Clone this repository:
    ```
    git clone https://github.com/daemonp/traefik-forklift-middleware.git
    cd traefik-forklift-middleware
    ```

## Configuration Options

The middleware supports the following configuration options:

-   `v1Backend`: The default backend URL
-   `v2Backend`: The new version backend URL
-   `rules`: An array of routing rules
    -   `path`: The exact request path to match
    -   `pathPrefix`: A prefix to match for the request path
    -   `method`: The HTTP method to match
    -   `conditions`: An array of conditions to match
        -   `type`: Type of the condition ("query", "form", "header")
        -   `parameter`: The parameter name to check
        -   `queryParam`: The query parameter to check (for type "query")
        -   `operator`: Comparison operator ("eq", "ne", "gt", "lt", "contains", "regex")
        -   `value`: The value to compare against
    -   `backend`: The backend URL to route to if the rule matches
    -   `percentage`: (Optional) Percentage of matching traffic to route to the specified backend
    -   `priority`: (Optional) Priority of the rule (higher numbers have higher priority)
    -   `pathPrefixRewrite`: (Optional) The new path prefix to rewrite the request to

## Use Cases and Examples

### Docker Compose

Here's an example of how to configure the middleware in a Docker Compose environment:

```yaml
version: "3"

services:
    traefik:
        image: traefik:v2.10
        command:
            - "--api.insecure=true"
            - "--providers.docker=true"
            - "--providers.docker.exposedbydefault=false"
            - "--entrypoints.web.address=:80"
            - "--experimental.localPlugins.abtest.moduleName=github.com/daemonp/traefik-forklift-middleware"
        ports:
            - "80:80"
            - "8080:8080"
        volumes:
            - /var/run/docker.sock:/var/run/docker.sock:ro
            - ./:/plugins-local/src/github.com/daemonp/traefik-forklift-middleware

    echo1:
        image: hashicorp/http-echo
        command: ["-text", "Hello from V1"]
        labels:
            - "traefik.enable=true"
            - "traefik.http.services.echo1.loadbalancer.server.port=5678"

    echo2:
        image: hashicorp/http-echo
        command: ["-text", "Hello from V2"]
        labels:
            - "traefik.enable=true"
            - "traefik.http.services.echo2.loadbalancer.server.port=5678"

    abtest:
        image: traefik/whoami
        labels:
            - "traefik.enable=true"
            - "traefik.http.routers.abtest.rule=PathPrefix(`/`)"
            - "traefik.http.routers.abtest.entrypoints=web"
            - "traefik.http.routers.abtest.middlewares=abtest-middleware"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.v1backend=http://echo1:5678"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.v2backend=http://echo2:5678"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[0].path=/"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[0].method=GET"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[0].percentage=50"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[1].path=/"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[1].method=POST"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[1].conditions[0].type=form"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[1].conditions[0].parameter=MID"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[1].conditions[0].operator=eq"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[1].conditions[0].value=a"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[1].percentage=100"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[2].path=/query-test"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[2].method=GET"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[2].conditions[0].type=query"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[2].conditions[0].queryParam=mid"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[2].conditions[0].operator=eq"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[2].conditions[0].value=two"
            - "traefik.http.middlewares.abtest-middleware.plugin.abtest.rules[2].backend=http://echo2:5678"
```

This configuration demonstrates several use cases:

1. Basic A/B testing with a 50/50 split for GET requests to the root path.
2. Routing all POST requests with a specific form parameter to V2.
3. Routing GET requests with a specific query parameter to V2.

### Kubernetes

For Kubernetes, you can use the middleware with IngressRoute CRDs. Here's an example:

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: abtest-middleware
spec:
    plugin:
        abtest:
            v1Backend: "http://v1-service"
            v2Backend: "http://v2-service"
            rules:
                - path: "/"
                  method: "GET"
                  percentage: 50
                - path: "/"
                  method: "POST"
                  conditions:
                      - type: "form"
                        parameter: "MID"
                        operator: "eq"
                        value: "a"
                  percentage: 100
                - path: "/query-test"
                  method: "GET"
                  conditions:
                      - type: "query"
                        queryParam: "mid"
                        operator: "eq"
                        value: "two"
                  backend: "http://v2-service"

---
apiVersion: traefik.containo.us/v1alpha1
kind: IngressRoute
metadata:
    name: myapp
spec:
    entryPoints:
        - web
    routes:
        - match: PathPrefix(`/`)
          kind: Rule
          services:
              - name: default-backend
                port: 80
          middlewares:
              - name: abtest-middleware
```

## Advanced Use Cases

1. **Gradual Rollout**:

    ```yaml
    - path: "/new-feature"
      method: "GET"
      backend: "http://v2-service"
      percentage: 10
    ```

    This rule will send 10% of GET requests for "/new-feature" to the V2 backend.

2. **Path Prefix Rewriting**:

    ```yaml
    - pathPrefix: "/api/v1"
      method: "GET"
      backend: "http://v1-service"
      pathPrefixRewrite: "/api"
    ```

    This rule will rewrite requests from "/api/v1/_" to "/api/_" before sending them to the V1 backend.

3. **Header-based Routing**:

    ```yaml
    - path: "/mobile-app"
      method: "GET"
      conditions:
          - type: "header"
            parameter: "User-Agent"
            operator: "contains"
            value: "Mobile"
      backend: "http://mobile-service"
    ```

    This rule routes requests to a mobile-specific backend based on the User-Agent header.

4. **Complex Condition Matching**:

    ```yaml
    - path: "/premium-content"
      method: "GET"
      conditions:
          - type: "header"
            parameter: "Authorization"
            operator: "regex"
            value: "^Bearer premium-.*$"
          - type: "query"
            queryParam: "version"
            operator: "eq"
            value: "v2"
      backend: "http://premium-v2-service"
    ```

    This rule combines header and query parameter conditions to route premium users to a specific backend.

5. **Priority-based Routing**:

    ```yaml
    - path: "/api"
      method: "GET"
      backend: "http://v1-service"
      priority: 1
    - pathPrefix: "/api"
      method: "GET"
      backend: "http://v2-service"
      priority: 2
    ```

    The rule with higher priority (2) will be evaluated first, allowing more specific rules to take precedence.

6. **Form Data Routing for POST Requests**:

    ```yaml
    - path: "/submit-form"
      method: "POST"
      conditions:
          - type: "form"
            parameter: "userType"
            operator: "eq"
            value: "beta"
      backend: "http://beta-form-handler"
    ```

    This rule routes POST requests with a specific form field value to a dedicated backend.

7. **Combining Percentage and Conditions**:
    ```yaml
    - path: "/new-checkout"
      method: "GET"
      conditions:
          - type: "cookie"
            parameter: "user_segment"
            operator: "eq"
            value: "high_value"
      backend: "http://v2-checkout"
      percentage: 50
    ```
    This rule sends 50% of high-value users (determined by a cookie) to the new checkout process.

These advanced use cases demonstrate the flexibility and power of the Traefik Forklift A/B Testing Middleware. You can combine various conditions, use gradual rollouts, and implement complex routing logic to suit your specific needs in both Docker Compose and Kubernetes environments.

## Troubleshooting

-   Check Traefik logs for any plugin-related errors
-   Verify that the plugin binary is correctly mounted in the Traefik container or pod
-   Ensure that the Middleware resource is correctly configured
-   Check that the IngressRoute or Docker labels are properly set to use the middleware

## License

This project is licensed under the MIT License.
