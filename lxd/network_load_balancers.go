package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gorilla/mux"

	"github.com/canonical/lxd/lxd/auth"
	clusterRequest "github.com/canonical/lxd/lxd/cluster/request"
	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/lifecycle"
	"github.com/canonical/lxd/lxd/network"
	"github.com/canonical/lxd/lxd/project"
	"github.com/canonical/lxd/lxd/request"
	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/lxd/lxd/util"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/entity"
	"github.com/canonical/lxd/shared/version"
)

var networkLoadBalancersCmd = APIEndpoint{
	Path:        "networks/{networkName}/load-balancers",
	MetricsType: entity.TypeNetwork,

	Get:  APIEndpointAction{Handler: networkLoadBalancersGet, AccessHandler: networkAccessHandler(auth.EntitlementCanView)},
	Post: APIEndpointAction{Handler: networkLoadBalancersPost, AccessHandler: networkAccessHandler(auth.EntitlementCanEdit)},
}

var networkLoadBalancerCmd = APIEndpoint{
	Path:        "networks/{networkName}/load-balancers/{listenAddress}",
	MetricsType: entity.TypeNetwork,

	Delete: APIEndpointAction{Handler: networkLoadBalancerDelete, AccessHandler: networkAccessHandler(auth.EntitlementCanEdit)},
	Get:    APIEndpointAction{Handler: networkLoadBalancerGet, AccessHandler: networkAccessHandler(auth.EntitlementCanView)},
	Put:    APIEndpointAction{Handler: networkLoadBalancerPut, AccessHandler: networkAccessHandler(auth.EntitlementCanEdit)},
	Patch:  APIEndpointAction{Handler: networkLoadBalancerPut, AccessHandler: networkAccessHandler(auth.EntitlementCanEdit)},
}

// API endpoints

// swagger:operation GET /1.0/networks/{networkName}/load-balancers network-load-balancers network_load_balancers_get
//
//  Get the network address of load balancers
//
//  Returns a list of network address load balancers (URLs).
//
//  ---
//  produces:
//    - application/json
//  parameters:
//    - in: query
//      name: project
//      description: Project name
//      type: string
//      example: default
//  responses:
//    "200":
//      description: API endpoints
//      schema:
//        type: object
//        description: Sync response
//        properties:
//          type:
//            type: string
//            description: Response type
//            example: sync
//          status:
//            type: string
//            description: Status description
//            example: Success
//          status_code:
//            type: integer
//            description: Status code
//            example: 200
//          metadata:
//            type: array
//            description: List of endpoints
//            items:
//              type: string
//            example: |-
//              [
//                "/1.0/networks/lxdbr0/load-balancers/192.0.2.1",
//                "/1.0/networks/lxdbr0/load-balancers/192.0.2.2"
//              ]
//    "403":
//      $ref: "#/responses/Forbidden"
//    "500":
//      $ref: "#/responses/InternalServerError"

// swagger:operation GET /1.0/networks/{networkName}/load-balancers?recursion=1 network-load-balancers network_load_balancer_get_recursion1
//
//	Get the network address load balancers
//
//	Returns a list of network address load balancers (structs).
//
//	---
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	responses:
//	  "200":
//	    description: API endpoints
//	    schema:
//	      type: object
//	      description: Sync response
//	      properties:
//	        type:
//	          type: string
//	          description: Response type
//	          example: sync
//	        status:
//	          type: string
//	          description: Status description
//	          example: Success
//	        status_code:
//	          type: integer
//	          description: Status code
//	          example: 200
//	        metadata:
//	          type: array
//	          description: List of network address load balancers
//	          items:
//	            $ref: "#/definitions/NetworkLoadBalancer"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func networkLoadBalancersGet(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	var effectiveProjectName string
	reqInfo := request.GetContextInfo(r.Context())
	if reqInfo != nil {
		effectiveProjectName = reqInfo.EffectiveProjectName
	}

	details, err := request.GetContextValue[networkDetails](r.Context(), ctxNetworkDetails)
	if err != nil {
		return response.SmartError(err)
	}

	n, err := network.LoadByName(s, effectiveProjectName, details.networkName)
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed loading network: %w", err))
	}

	// Check if project allows access to network.
	if !project.NetworkAllowed(details.requestProject.Config, details.networkName, n.IsManaged()) {
		return response.SmartError(api.StatusErrorf(http.StatusNotFound, "Network not found"))
	}

	if !n.Info().LoadBalancers {
		return response.BadRequest(fmt.Errorf("Network driver %q does not support load balancers", n.Type()))
	}

	memberSpecific := false // Get load balancers for all cluster members.

	if util.IsRecursionRequest(r) {
		var records map[int64]*api.NetworkLoadBalancer

		err = s.DB.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
			records, err = tx.GetNetworkLoadBalancers(ctx, n.ID(), memberSpecific)

			return err
		})
		if err != nil {
			return response.SmartError(fmt.Errorf("Failed loading network load balancers: %w", err))
		}

		loadBalancers := make([]*api.NetworkLoadBalancer, 0, len(records))
		for _, record := range records {
			loadBalancers = append(loadBalancers, record)
		}

		return response.SyncResponse(true, loadBalancers)
	}

	var listenAddresses map[int64]string

	err = s.DB.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
		listenAddresses, err = tx.GetNetworkLoadBalancerListenAddresses(ctx, n.ID(), memberSpecific)

		return err
	})
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed loading network load balancers: %w", err))
	}

	loadBalancerURLs := make([]string, 0, len(listenAddresses))
	for _, listenAddress := range listenAddresses {
		u := api.NewURL().Path(version.APIVersion, "networks", n.Name(), "load-balancers", listenAddress)
		loadBalancerURLs = append(loadBalancerURLs, u.String())
	}

	return response.SyncResponse(true, loadBalancerURLs)
}

// swagger:operation POST /1.0/networks/{networkName}/load-balancers network-load-balancers network_load_balancers_post
//
//	Add a network load balancer
//
//	Creates a new network load balancer.
//
//	---
//	consumes:
//	  - application/json
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	  - in: body
//	    name: load-balancer
//	    description: Load Balancer
//	    required: true
//	    schema:
//	      $ref: "#/definitions/NetworkLoadBalancersPost"
//	responses:
//	  "200":
//	    $ref: "#/responses/EmptySyncResponse"
//	  "400":
//	    $ref: "#/responses/BadRequest"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func networkLoadBalancersPost(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	target := request.QueryParam(r, "target")
	resp := forwardedResponseToNode(r.Context(), s, target)
	if resp != nil {
		return resp
	}

	var effectiveProjectName string
	reqInfo := request.GetContextInfo(r.Context())
	if reqInfo != nil {
		effectiveProjectName = reqInfo.EffectiveProjectName
	}

	details, err := request.GetContextValue[networkDetails](r.Context(), ctxNetworkDetails)
	if err != nil {
		return response.SmartError(err)
	}

	// Parse the request into a record.
	req := api.NetworkLoadBalancersPost{}
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		return response.BadRequest(err)
	}

	req.Normalise() // So we handle the request in normalised/canonical form.

	n, err := network.LoadByName(s, effectiveProjectName, details.networkName)
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed loading network: %w", err))
	}

	// Check if project allows access to network.
	if !project.NetworkAllowed(details.requestProject.Config, details.networkName, n.IsManaged()) {
		return response.SmartError(api.StatusErrorf(http.StatusNotFound, "Network not found"))
	}

	if !n.Info().LoadBalancers {
		return response.BadRequest(fmt.Errorf("Network driver %q does not support load balancers", n.Type()))
	}

	clientType := clusterRequest.UserAgentClientType(r.Header.Get("User-Agent"))

	listenAddress, err := n.LoadBalancerCreate(req, clientType)
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed creating load balancer: %w", err))
	}

	lc := lifecycle.NetworkLoadBalancerCreated.Event(n, listenAddress.String(), request.CreateRequestor(r.Context()), nil)
	s.Events.SendLifecycle(effectiveProjectName, lc)

	return response.SyncResponseLocation(true, nil, lc.Source)
}

// swagger:operation DELETE /1.0/networks/{networkName}/load-balancers/{listenAddress} network-load-balancers network_load_balancer_delete
//
//	Delete the network address load balancer
//
//	Removes the network address load balancer.
//
//	---
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	responses:
//	  "200":
//	    $ref: "#/responses/EmptySyncResponse"
//	  "400":
//	    $ref: "#/responses/BadRequest"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func networkLoadBalancerDelete(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	target := request.QueryParam(r, "target")
	resp := forwardedResponseToNode(r.Context(), s, target)
	if resp != nil {
		return resp
	}

	var effectiveProjectName string
	reqInfo := request.GetContextInfo(r.Context())
	if reqInfo != nil {
		effectiveProjectName = reqInfo.EffectiveProjectName
	}

	details, err := request.GetContextValue[networkDetails](r.Context(), ctxNetworkDetails)
	if err != nil {
		return response.SmartError(err)
	}

	n, err := network.LoadByName(s, effectiveProjectName, details.networkName)
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed loading network: %w", err))
	}

	// Check if project allows access to network.
	if !project.NetworkAllowed(details.requestProject.Config, details.networkName, n.IsManaged()) {
		return response.SmartError(api.StatusErrorf(http.StatusNotFound, "Network not found"))
	}

	if !n.Info().LoadBalancers {
		return response.BadRequest(fmt.Errorf("Network driver %q does not support load balancers", n.Type()))
	}

	listenAddress, err := url.PathUnescape(mux.Vars(r)["listenAddress"])
	if err != nil {
		return response.SmartError(err)
	}

	clientType := clusterRequest.UserAgentClientType(r.Header.Get("User-Agent"))

	err = n.LoadBalancerDelete(listenAddress, clientType)
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed deleting load balancer: %w", err))
	}

	s.Events.SendLifecycle(effectiveProjectName, lifecycle.NetworkLoadBalancerDeleted.Event(n, listenAddress, request.CreateRequestor(r.Context()), nil))

	return response.EmptySyncResponse
}

// swagger:operation GET /1.0/networks/{networkName}/load-balancers/{listenAddress} network-load-balancers network_load_balancer_get
//
//	Get the network address load balancer
//
//	Gets a specific network address load balancer.
//
//	---
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	responses:
//	  "200":
//	    description: Load Balancer
//	    schema:
//	      type: object
//	      description: Sync response
//	      properties:
//	        type:
//	          type: string
//	          description: Response type
//	          example: sync
//	        status:
//	          type: string
//	          description: Status description
//	          example: Success
//	        status_code:
//	          type: integer
//	          description: Status code
//	          example: 200
//	        metadata:
//	          $ref: "#/definitions/NetworkLoadBalancer"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func networkLoadBalancerGet(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	target := request.QueryParam(r, "target")
	resp := forwardedResponseToNode(r.Context(), s, target)
	if resp != nil {
		return resp
	}

	var effectiveProjectName string
	reqInfo := request.GetContextInfo(r.Context())
	if reqInfo != nil {
		effectiveProjectName = reqInfo.EffectiveProjectName
	}

	details, err := request.GetContextValue[networkDetails](r.Context(), ctxNetworkDetails)
	if err != nil {
		return response.SmartError(err)
	}

	n, err := network.LoadByName(s, effectiveProjectName, details.networkName)
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed loading network: %w", err))
	}

	// Check if project allows access to network.
	if !project.NetworkAllowed(details.requestProject.Config, details.networkName, n.IsManaged()) {
		return response.SmartError(api.StatusErrorf(http.StatusNotFound, "Network not found"))
	}

	if !n.Info().LoadBalancers {
		return response.BadRequest(fmt.Errorf("Network driver %q does not support load balancers", n.Type()))
	}

	listenAddress, err := url.PathUnescape(mux.Vars(r)["listenAddress"])
	if err != nil {
		return response.SmartError(err)
	}

	targetMember := request.QueryParam(r, "target")
	memberSpecific := targetMember != ""

	var loadBalancer *api.NetworkLoadBalancer

	err = s.DB.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
		_, loadBalancer, err = tx.GetNetworkLoadBalancer(ctx, n.ID(), memberSpecific, listenAddress)

		return err
	})
	if err != nil {
		return response.SmartError(err)
	}

	return response.SyncResponseETag(true, loadBalancer, loadBalancer.Etag())
}

// swagger:operation PATCH /1.0/networks/{networkName}/load-balancers/{listenAddress} network-load-balancers network_load_balancer_patch
//
//  Partially update the network address load balancer
//
//  Updates a subset of the network address load balancer configuration.
//
//  ---
//  consumes:
//    - application/json
//  produces:
//    - application/json
//  parameters:
//    - in: query
//      name: project
//      description: Project name
//      type: string
//      example: default
//    - in: body
//      name: load-balancer
//      description: Address load balancer configuration
//      required: true
//      schema:
//        $ref: "#/definitions/NetworkLoadBalancerPut"
//  responses:
//    "200":
//      $ref: "#/responses/EmptySyncResponse"
//    "400":
//      $ref: "#/responses/BadRequest"
//    "403":
//      $ref: "#/responses/Forbidden"
//    "412":
//      $ref: "#/responses/PreconditionFailed"
//    "500":
//      $ref: "#/responses/InternalServerError"

// swagger:operation PUT /1.0/networks/{networkName}/load-balancers/{listenAddress} network-load-balancers network_load_balancer_put
//
//	Update the network address load balancer
//
//	Updates the entire network address load balancer configuration.
//
//	---
//	consumes:
//	  - application/json
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	  - in: body
//	    name: load-balancer
//	    description: Address load balancer configuration
//	    required: true
//	    schema:
//	      $ref: "#/definitions/NetworkLoadBalancerPut"
//	responses:
//	  "200":
//	    $ref: "#/responses/EmptySyncResponse"
//	  "400":
//	    $ref: "#/responses/BadRequest"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "412":
//	    $ref: "#/responses/PreconditionFailed"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func networkLoadBalancerPut(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	target := request.QueryParam(r, "target")
	resp := forwardedResponseToNode(r.Context(), s, target)
	if resp != nil {
		return resp
	}

	var effectiveProjectName string
	reqInfo := request.GetContextInfo(r.Context())
	if reqInfo != nil {
		effectiveProjectName = reqInfo.EffectiveProjectName
	}

	details, err := request.GetContextValue[networkDetails](r.Context(), ctxNetworkDetails)
	if err != nil {
		return response.SmartError(err)
	}

	n, err := network.LoadByName(s, effectiveProjectName, details.networkName)
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed loading network: %w", err))
	}

	// Check if project allows access to network.
	if !project.NetworkAllowed(details.requestProject.Config, details.networkName, n.IsManaged()) {
		return response.SmartError(api.StatusErrorf(http.StatusNotFound, "Network not found"))
	}

	if !n.Info().LoadBalancers {
		return response.BadRequest(fmt.Errorf("Network driver %q does not support load balancers", n.Type()))
	}

	listenAddress, err := url.PathUnescape(mux.Vars(r)["listenAddress"])
	if err != nil {
		return response.SmartError(err)
	}

	// Decode the request.
	req := api.NetworkLoadBalancerPut{}
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		return response.BadRequest(err)
	}

	targetMember := request.QueryParam(r, "target")
	memberSpecific := targetMember != ""

	if r.Method == http.MethodPatch {
		var loadBalancer *api.NetworkLoadBalancer

		err = s.DB.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
			_, loadBalancer, err = tx.GetNetworkLoadBalancer(ctx, n.ID(), memberSpecific, listenAddress)

			return err
		})
		if err != nil {
			return response.SmartError(err)
		}

		// If config being updated via "patch" method, then merge all existing config with the keys that
		// are present in the request config.
		for k, v := range loadBalancer.Config {
			_, ok := req.Config[k]
			if !ok {
				req.Config[k] = v
			}
		}

		// If load balancer being updated via "patch" method and backends not specified, then merge
		// existing backends into load balancer.
		if req.Backends == nil {
			req.Backends = loadBalancer.Backends
		}

		// If load balancer being updated via "patch" method and ports not specified, then merge existing
		// ports into load balancer.
		if req.Ports == nil {
			req.Ports = loadBalancer.Ports
		}
	}

	req.Normalise() // So we handle the request in normalised/canonical form.

	clientType := clusterRequest.UserAgentClientType(r.Header.Get("User-Agent"))

	err = n.LoadBalancerUpdate(listenAddress, req, clientType)
	if err != nil {
		return response.SmartError(fmt.Errorf("Failed updating load balancer: %w", err))
	}

	s.Events.SendLifecycle(effectiveProjectName, lifecycle.NetworkLoadBalancerUpdated.Event(n, listenAddress, request.CreateRequestor(r.Context()), nil))

	return response.EmptySyncResponse
}
