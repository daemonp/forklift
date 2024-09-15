# Traefik Forklift A/B Testing Middleware

## Introduction

Traefik Forklift is a powerful middleware plugin for Traefik that enables advanced A/B testing, canary deployments, and traffic routing strategies. It allows you to route traffic to different backend services based on a wide range of conditions, including request paths, methods, headers, query parameters, form data, cookies, and more.

## Features

-   **Dynamic Routing Rules:** Define flexible routing rules based on various request attributes.
-   **Percentage-Based Traffic Splitting:** Gradually roll out new features or services by controlling the percentage of traffic directed to different backends.
-   **Default Backend Support:** Specify a default backend to handle requests that don't match any rules.
-   **Session Affinity:** Maintain consistent routing decisions for users based on session IDs.
-   **Path Prefix Rewriting:** Modify request paths before they reach the backend services.
-   **Priority-Based Rule Evaluation:** Control the order in which rules are evaluated using priorities.

## Prerequisites

-   **Traefik v2.5 or later**
-   **Kubernetes Cluster** (for Kubernetes deployment)
-   **Go 1.16 or later** (if building from source)

## Configuration Options

### Global Configuration

-   **`defaultBackend`** (string, required): The default backend URL to use when no rule matches.

### Routing Rules

Each rule in the `rules` array supports the following fields:

-   **`path`** (string, optional): Exact request path to match.
-   **`pathPrefix`** (string, optional): Request path prefix to match.
-   **`method`** (string, optional): HTTP method to match (e.g., GET, POST).
-   **`conditions`** (array of conditions, optional): Additional conditions to match.
    -   **`type`** (string): Type of condition (`header`, `query`, `form`, `cookie`).
    -   **`parameter`** (string): The name of the header, form field, or cookie.
    -   **`queryParam`** (string): The name of the query parameter (for type `query`).
    -   **`operator`** (string): Comparison operator (`eq`, `contains`, `regex`, `gt`, `lt`, etc.).
    -   **`value`** (string): The value to compare against.
-   **`backend`** (string, required): Backend URL to route to if the rule matches.
-   **`percentage`** (float, optional): Percentage of traffic to route to this backend (used when multiple rules match).
-   **`priority`** (int, optional): Priority of the rule (higher numbers are evaluated first).
-   **`pathPrefixRewrite`** (string, optional): New path prefix to rewrite the request to before forwarding.

## Kubernetes Examples

Below are Kubernetes examples demonstrating various configuration options. Each example corresponds to specific test cases and demonstrates how to configure the middleware for different routing scenarios.

### 1. Basic GET Routing with Traffic Splitting

**Scenario:** Split GET requests to `/` between two backends (`v1-service` and `v2-service`) equally.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: abtest-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://default-service"
            rules:
                - path: "/"
                  method: "GET"
                  backend: "http://v1-service"
                  percentage: 50
                  priority: 1
                - path: "/"
                  method: "GET"
                  backend: "http://v2-service"
                  percentage: 50
                  priority: 1
```

**Explanation:**

-   Defines two rules for GET `/` with equal percentages.
-   Traffic is split 50/50 between `v1-service` and `v2-service`.
-   Both rules have the same priority.

### 2. Routing Based on Form Data in POST Requests

**Scenario:** Route POST requests to `/` with form parameter `MID=a` to `v2-service`.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: form-routing-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://default-service"
            rules:
                - path: "/"
                  method: "POST"
                  backend: "http://v2-service"
                  priority: 1
                  conditions:
                      - type: "form"
                        parameter: "MID"
                        operator: "eq"
                        value: "a"
```

**Explanation:**

-   Routes POST requests with form field `MID=a` to `v2-service`.
-   Uses a condition of type `form` to inspect form data.

### 3. Routing Based on Query Parameters

**Scenario:** Route GET requests to `/query-test` with query parameter `mid=two` to `v2-service`.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: query-routing-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://default-service"
            rules:
                - path: "/query-test"
                  method: "GET"
                  backend: "http://v2-service"
                  priority: 1
                  conditions:
                      - type: "query"
                        queryParam: "mid"
                        operator: "eq"
                        value: "two"
```

**Explanation:**

-   Routes GET requests with `mid=two` in the query string to `v2-service`.
-   Uses a condition of type `query` to inspect query parameters.

### 4. Percentage-Based Routing with Form Data

**Scenario:** Route 10% of POST requests to `/` with form parameter `MID=d` to `v3-service`.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: percentage-form-routing-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://default-service"
            rules:
                - path: "/"
                  method: "POST"
                  backend: "http://v3-service"
                  percentage: 10
                  priority: 1
                  conditions:
                      - type: "form"
                        parameter: "MID"
                        operator: "eq"
                        value: "d"
```

**Explanation:**

-   Routes 10% of matching requests to `v3-service`, allowing for a gradual rollout.
-   The remaining 90% will follow other matching rules or default to the `defaultBackend`.

### 5. Path Prefix Rewriting

**Scenario:** Rewrite path prefix from `/api/v1` to `/v1` before forwarding to the backend.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: path-prefix-rewrite-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://default-service"
            rules:
                - pathPrefix: "/api/v1"
                  method: "GET"
                  backend: "http://v1-service"
                  pathPrefixRewrite: "/v1"
                  priority: 1
```

**Explanation:**

-   Matches any path starting with `/api/v1`.
-   Rewrites the path prefix to `/v1` before sending to `v1-service`.

### 6. Header-Based Routing

**Scenario:** Route GET requests to `/language` with `Accept-Language` header containing `es` to `v2-service`.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: header-routing-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://default-service"
            rules:
                - path: "/language"
                  method: "GET"
                  backend: "http://v2-service"
                  priority: 1
                  conditions:
                      - type: "header"
                        parameter: "Accept-Language"
                        operator: "contains"
                        value: "es"
```

**Explanation:**

-   Routes requests based on the `Accept-Language` header.
-   Useful for serving localized content.

### 7. Priority-Based Rule Evaluation

**Scenario:** Define multiple rules for `/priority-test` with different priorities.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: priority-routing-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://default-service"
            rules:
                - path: "/priority-test"
                  method: "GET"
                  backend: "http://v1-service"
                  priority: 10
                - path: "/priority-test"
                  method: "GET"
                  backend: "http://v2-service"
                  priority: 5
```

**Explanation:**

-   The rule with higher priority (10) is evaluated first.
-   Requests to `/priority-test` will be routed to `v1-service` due to the higher priority.

### 8. Session Affinity

**Scenario:** Ensure that users with the same session ID are consistently routed to the same backend.

**Note:** Session affinity is automatically handled by the middleware using a session cookie named `forklift_id`. No additional configuration is required in the rules. Just define your rules as needed, and the middleware will ensure consistent routing based on the session ID.

### 9. Complex Condition Matching

**Scenario:** Route requests to `/premium-content` based on header and query parameter conditions.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: complex-condition-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://default-service"
            rules:
                - path: "/premium-content"
                  method: "GET"
                  backend: "http://premium-service"
                  priority: 1
                  conditions:
                      - type: "header"
                        parameter: "Authorization"
                        operator: "regex"
                        value: "^Bearer premium-.*$"
                      - type: "query"
                        queryParam: "version"
                        operator: "eq"
                        value: "v2"
```

**Explanation:**

-   Combines header and query parameter conditions.
-   Uses regex operator to match authorization tokens.
-   Only routes to `premium-service` if both conditions are met.

### 10. Combining Percentage and Conditions

**Scenario:** Send 50% of high-value users (determined by a cookie) to a new checkout process.

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: Middleware
metadata:
    name: percentage-cookie-routing-middleware
spec:
    plugin:
        abtest:
            defaultBackend: "http://checkout-service"
            rules:
                - path: "/new-checkout"
                  method: "GET"
                  backend: "http://v2-checkout-service"
                  percentage: 50
                  priority: 1
                  conditions:
                      - type: "cookie"
                        parameter: "user_segment"
                        operator: "eq"
                        value: "high_value"
```

**Explanation:**

-   Routes 50% of requests from users with `user_segment=high_value` cookie to `v2-checkout-service`.
-   Useful for testing new features with a subset of valuable users.

## Applying the Middleware in Kubernetes

To apply the middleware and use it with your IngressRoutes, you need to create the middleware resource and reference it in your ingress configurations.

### Example IngressRoute

```yaml
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
              - name: default-service
                port: 80
          middlewares:
              - name: abtest-middleware
```

**Explanation:**

-   References the middleware named `abtest-middleware`.
-   All requests matching the route will be processed by the middleware before reaching the service.

## Advanced Use Cases

### A. Gradual Feature Rollouts with Percentage-Based Routing

**Scenario:** Gradually roll out a new feature by increasing the percentage over time.

```yaml
# Initial rollout with 10%
rules:
  - path: "/new-feature"
    method: "GET"
    backend: "http://v2-service"
    percentage: 10
    priority: 1

# Later, increase to 30%
rules:
  - path: "/new-feature"
    method: "GET"
    backend: "http://v2-service"
    percentage: 30
    priority: 1
```

**Explanation:**

-   Adjust the `percentage` field over time to control the rollout.

### B. A/B Testing with Multiple Conditions

**Scenario:** Perform A/B testing based on multiple user attributes.

```yaml
rules:
    - path: "/experiment"
      method: "GET"
      backend: "http://variant-a-service"
      percentage: 50
      priority: 1
      conditions:
          - type: "header"
            parameter: "User-Agent"
            operator: "contains"
            value: "Mobile"
    - path: "/experiment"
      method: "GET"
      backend: "http://variant-b-service"
      percentage: 50
      priority: 1
      conditions:
          - type: "header"
            parameter: "User-Agent"
            operator: "contains"
            value: "Desktop"
```

**Explanation:**

-   Splits traffic between two variants based on the `User-Agent` header.
-   Each variant receives 50% of the traffic matching its condition.

## Troubleshooting

-   **Check Traefik Logs:** Enable debug mode to see detailed logs from the middleware.
-   **Verify Configuration:** Ensure that your Kubernetes resources are correctly defined and applied.
-   **Session IDs:** Confirm that session cookies are properly set and used if session affinity is important for your use case.
-   **Percentage Sum:** Ensure that the percentages in matching rules sum up to 100% if you want full traffic distribution among backends.

## Additional Notes

-   **Middleware Name:** The name you give to the middleware resource (e.g., `abtest-middleware`) must match the name referenced in your `IngressRoute`.
-   **Plugin Availability:** Ensure that the Traefik plugin is available and correctly configured in your Traefik deployment. This may require adding the plugin to your Traefik static configuration.
-   **Order of Evaluation:** Rules are evaluated based on their `priority`. Higher priority rules are evaluated first.

## License

This project is licensed under the MIT License.

---

**Note:** When applying these configurations to your Kubernetes cluster, ensure that:

-   The services referenced in the `backend` fields are accessible and correctly defined.
-   The plugin is enabled in your Traefik deployment.
-   Any sensitive values are appropriately secured (e.g., using Kubernetes secrets for backend URLs if needed).
