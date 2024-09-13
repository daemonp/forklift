# Traefik Forklift A/B Testing Middleware

This Traefik middleware plugin enables advanced A/B testing capabilities for your Kubernetes ingress. It allows you to route traffic to different backend services based on various conditions, including request path, method, query parameters, form data, and headers.

## Features

-   Dynamic routing based on multiple conditions
-   Support for gradual rollout with percentage-based routing
-   Flexible configuration using Traefik's middleware plugin system
-   Compatible with Kubernetes ingress

## Prerequisites

-   Go 1.16 or later
-   Traefik v2.5 or later
-   Kubernetes cluster (if deploying to Kubernetes)

## Building the Plugin

1. Clone this repository:

    ```
    git clone https://github.com/daemonp/traefik-forklift-middleware.git
    cd traefik-forklift-middleware
    ```

2. Build the plugin:
    ```
    go build -buildmode=plugin -o abtest.so
    ```

## Deploying the Plugin

### Local Traefik Instance

1. Move the built plugin to Traefik's plugin directory:

    ```
    mkdir -p /path/to/traefik/plugins/abtest
    mv abtest.so /path/to/traefik/plugins/abtest/
    ```

2. Update your Traefik static configuration to enable the plugin:
    ```yaml
    experimental:
        plugins:
            abtest:
                moduleName: "github.com/your-repo/traefik-abtest-middleware"
                version: "v1.0.0"
    ```

### Kubernetes Deployment

1. Create a ConfigMap with the plugin binary:

    ```yaml
    apiVersion: v1
    kind: ConfigMap
    metadata:
        name: abtest-plugin
        namespace: traefik
    binaryData:
        abtest.so: <base64-encoded plugin binary>
    ```

2. Update your Traefik deployment to mount the plugin:

    ```yaml
    apiVersion: apps/v1
    kind: Deployment
    metadata:
        name: traefik
        namespace: traefik
    spec:
        template:
            spec:
                containers:
                    - name: traefik
                      volumeMounts:
                          - name: plugins
                            mountPath: /plugins
                volumes:
                    - name: plugins
                      configMap:
                          name: abtest-plugin
    ```

3. Update Traefik's static configuration to enable the plugin:
    ```yaml
    experimental:
        plugins:
            abtest:
                moduleName: "github.com/your-repo/traefik-abtest-middleware"
                version: "v1.0.0"
    ```

## Configuring the Middleware

Create a Middleware resource to configure the A/B testing rules:

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: my-abtest
    namespace: default
spec:
    plugin:
        abtest:
            v1Backend: "http://v1-backend.prod"
            v2Backend: "http://v2-backend.prod"
            rules:
                # POST request example
                - path: "/consumer/InteracGateway.do"
                  method: "POST"
                  conditions:
                      - type: "form"
                        parameter: "merchantId"
                        operator: "eq"
                        value: "MERCHANT_A"
                  backend: "http://v2-backend.prod"

                # GET request example with query parameter
                - path: "/consumer/AccountInfo"
                  method: "GET"
                  conditions:
                      - type: "query"
                        parameter: "customerId"
                        operator: "regex"
                        value: "^VIP-"
                  backend: "http://v2-backend.prod"

                # GET request example with header-based routing
                - path: "/api/products"
                  method: "GET"
                  conditions:
                      - type: "header"
                        parameter: "User-Agent"
                        operator: "contains"
                        value: "Mobile"
                  backend: "http://v2-backend.prod"
                  percentage: 0.3

                # POST request example with transaction amount
                - path: "/consumer/InteracGateway.do"
                  method: "POST"
                  conditions:
                      - type: "form"
                        parameter: "txnAmount"
                        operator: "gt"
                        value: "1000"
                  backend: "http://v2-backend.prod"
                  percentage: 0.5

                # GET request example with multiple conditions
                - path: "/api/dashboard"
                  method: "GET"
                  conditions:
                      - type: "query"
                        parameter: "version"
                        operator: "eq"
                        value: "beta"
                      - type: "header"
                        parameter: "X-Beta-Tester"
                        operator: "eq"
                        value: "true"
                  backend: "http://v2-backend.prod"

                # POST request example for language-based routing
                - path: "/consumer/RequestMoneyCreationController.do"
                  method: "POST"
                  conditions:
                      - type: "query"
                        parameter: "lang"
                        operator: "eq"
                        value: "fr"
                  backend: "http://v2-backend.prod"
```

## Using the Middleware with Ingress

Apply the middleware to your Ingress resource:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
    name: my-ingress
    annotations:
        traefik.ingress.kubernetes.io/router.middlewares: default-my-abtest@kubernetescrd
spec:
    rules:
        - host: example.com
          http:
              paths:
                  - path: /consumer
                    pathType: Prefix
                    backend:
                        service:
                            name: default-backend
                            port:
                                number: 80
```

## Configuration Options

-   `v1Backend`: The default backend URL
-   `v2Backend`: The new version backend URL
-   `rules`: An array of routing rules
    -   `path`: The request path to match (supports regex)
    -   `method`: The HTTP method to match
    -   `conditions`: An array of conditions to match
        -   `type`: Type of the condition ("query", "form", or "header")
        -   `parameter`: The parameter name to check
        -   `operator`: Comparison operator ("eq", "ne", "gt", "lt", "contains", "regex")
        -   `value`: The value to compare against
    -   `backend`: The backend URL to route to if the rule matches
    -   `percentage`: (Optional) Percentage of matching traffic to route to the specified backend

## Troubleshooting

-   Check Traefik logs for any plugin-related errors
-   Verify that the plugin binary is correctly mounted in the Traefik pod
-   Ensure that the Middleware resource is correctly configured
-   Check that the Ingress resource is properly annotated to use the middleware

## Contributing

Contributions are welcome! Please submit pull requests or open issues on the GitHub repository.

## License

This project is licensed under the MIT License.
