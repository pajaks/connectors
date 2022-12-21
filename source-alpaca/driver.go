package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v2/marketdata"
	schemagen "github.com/estuary/connectors/go-schema-gen"
	pc "github.com/estuary/flow/go/protocols/capture"
	pf "github.com/estuary/flow/go/protocols/flow"
)

type driver struct{}

func (driver) Spec(ctx context.Context, req *pc.SpecRequest) (*pc.SpecResponse, error) {
	var endpointSchema, err = schemagen.GenerateSchema("Source Alpaca Spec", &config{}).MarshalJSON()
	if err != nil {
		fmt.Println(fmt.Errorf("generating endpoint schema: %w", err))
	}
	resourceSchema, err := schemagen.GenerateSchema("Trade Document", &resource{}).MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("generating resource schema: %w", err)
	}

	return &pc.SpecResponse{
		EndpointSpecSchemaJson: json.RawMessage(endpointSchema),
		ResourceSpecSchemaJson: json.RawMessage(resourceSchema),
		DocumentationUrl:       "https://go.estuary.dev/source-alpaca",
	}, nil
}

func resourceWithConfigDefaults(res resource, cfg config) resource {
	out := res

	// If there are no explicit values from the resource configuration, fall back to the values from
	// the endpoint configuration.
	if out.Feed == "" {
		out.Feed = cfg.Feed
	}
	if out.Symbols == "" {
		out.Symbols = cfg.Symbols
	}

	return out
}

func (driver) Validate(ctx context.Context, req *pc.ValidateRequest) (*pc.ValidateResponse, error) {
	var cfg config
	if err := pf.UnmarshalStrict(req.EndpointSpecJson, &cfg); err != nil {
		return nil, fmt.Errorf("parsing endpoint config: %w", err)
	}

	var out []*pc.ValidateResponse_Binding
	feeds := make(map[string][]string)
	for _, binding := range req.Bindings {
		var res resource
		if err := pf.UnmarshalStrict(binding.ResourceSpecJson, &res); err != nil {
			return nil, fmt.Errorf("parsing resource config: %w", err)
		}
		res = resourceWithConfigDefaults(res, cfg)

		out = append(out, &pc.ValidateResponse_Binding{
			ResourcePath: []string{res.Name},
		})

		feeds[res.Feed] = res.GetSymbols()
	}

	// Validate that a connection can be made to Alpaca API for all of the specified feeds. We need
	// to provide a symbol for the test request, so just use the first one from the list of symbols
	// for the binding. Validation ensures that there will be at least one symbol in the list.

	// Query an arbitrary time in the past so that we don't have to worry about the "free plan"
	// option which cannot query within the last 15 minutes.
	testTime := time.Now().Add(-1 * time.Hour)
	for feed, symbols := range feeds {
		_, err := marketdata.NewClient(marketdata.ClientOpts{
			ApiKey:    cfg.ApiKeyID,
			ApiSecret: cfg.ApiSecretKey,
		}).GetTrades(symbols[0], marketdata.GetTradesParams{Feed: feed, Start: testTime, End: testTime})

		if err != nil {
			return nil, fmt.Errorf("error when connecting to feed %s: %w", feed, err)
		}
	}

	return &pc.ValidateResponse{Bindings: out}, nil
}

func (driver) Discover(ctx context.Context, req *pc.DiscoverRequest) (*pc.DiscoverResponse, error) {
	var cfg config
	if err := pf.UnmarshalStrict(req.EndpointSpecJson, &cfg); err != nil {
		return nil, fmt.Errorf("parsing endpoint config: %w", err)
	}

	documentSchema, err := schemagen.GenerateSchema("", &tradeDocument{}).MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("generating document schema: %w", err)
	}

	resourceJSON, err := json.Marshal(resource{
		Name:    "trades",
		Feed:    cfg.Feed,
		Symbols: cfg.Symbols,
	})
	if err != nil {
		return nil, fmt.Errorf("serializing resource json: %w", err)
	}

	return &pc.DiscoverResponse{
		Bindings: []*pc.DiscoverResponse_Binding{{
			RecommendedName:    "trades",
			ResourceSpecJson:   resourceJSON,
			DocumentSchemaJson: documentSchema,
			KeyPtrs:            []string{"/ID", "/Symbol", "/Exchange", "/Timestamp"},
		}},
	}, nil
}

func (d driver) ApplyUpsert(ctx context.Context, req *pc.ApplyRequest) (*pc.ApplyResponse, error) {
	return d.apply(ctx, req, false)
}

func (d driver) ApplyDelete(ctx context.Context, req *pc.ApplyRequest) (*pc.ApplyResponse, error) {
	return d.apply(ctx, req, true)
}

func (driver) apply(ctx context.Context, req *pc.ApplyRequest, isDelete bool) (*pc.ApplyResponse, error) {
	return &pc.ApplyResponse{
		ActionDescription: "",
	}, nil
}
