package azurerm

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/Godeps/_workspace/src/github.com/Azure/go-autorest/autorest"
	"github.com/hashicorp/terraform/helper/mutexkv"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/terraform"
)

// Provider returns a terraform.ResourceProvider.
func Provider() terraform.ResourceProvider {
	return &schema.Provider{
		Schema: map[string]*schema.Schema{
			"subscription_id": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				DefaultFunc: schema.EnvDefaultFunc("ARM_SUBSCRIPTION_ID", ""),
			},

			"client_id": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				DefaultFunc: schema.EnvDefaultFunc("ARM_CLIENT_ID", ""),
			},

			"client_secret": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				DefaultFunc: schema.EnvDefaultFunc("ARM_CLIENT_SECRET", ""),
			},

			"tenant_id": &schema.Schema{
				Type:        schema.TypeString,
				Required:    true,
				DefaultFunc: schema.EnvDefaultFunc("ARM_TENANT_ID", ""),
			},
		},

		ResourcesMap: map[string]*schema.Resource{
			"azurerm_resource_group":         resourceArmResourceGroup(),
			"azurerm_virtual_network":        resourceArmVirtualNetwork(),
			"azurerm_local_network_gateway":  resourceArmLocalNetworkGateway(),
			"azurerm_availability_set":       resourceArmAvailabilitySet(),
			"azurerm_network_security_group": resourceArmNetworkSecurityGroup(),
			"azurerm_network_security_rule":  resourceArmNetworkSecurityRule(),
			"azurerm_public_ip":              resourceArmPublicIp(),
			"azurerm_subnet":                 resourceArmSubnet(),
			"azurerm_network_interface":      resourceArmNetworkInterface(),
			"azurerm_route_table":            resourceArmRouteTable(),
			"azurerm_route":                  resourceArmRoute(),
			"azurerm_cdn_profile":            resourceArmCdnProfile(),
			"azurerm_cdn_endpoint":           resourceArmCdnEndpoint(),
		},
		ConfigureFunc: providerConfigure,
	}
}

// Config is the configuration structure used to instantiate a
// new Azure management client.
type Config struct {
	ManagementURL string

	SubscriptionID string
	ClientID       string
	ClientSecret   string
	TenantID       string
}

func providerConfigure(d *schema.ResourceData) (interface{}, error) {
	config := Config{
		SubscriptionID: d.Get("subscription_id").(string),
		ClientID:       d.Get("client_id").(string),
		ClientSecret:   d.Get("client_secret").(string),
		TenantID:       d.Get("tenant_id").(string),
	}

	client, err := config.getArmClient()
	if err != nil {
		return nil, err
	}

	err = registerAzureResourceProvidersWithSubscription(&config, client)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// registerAzureResourceProvidersWithSubscription uses the providers client to register
// all Azure resource providers which the Terraform provider may require (regardless of
// whether they are actually used by the configuration or not). It was confirmed by Microsoft
// that this is the approach their own internal tools also take.
func registerAzureResourceProvidersWithSubscription(config *Config, client *ArmClient) error {
	providerClient := client.providers

	providers := []string{"Microsoft.Network", "Microsoft.Compute", "Microsoft.Cdn"}

	for _, v := range providers {
		res, err := providerClient.Register(v)
		if err != nil {
			return err
		}

		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("Error registering provider %q with subscription %q", v, config.SubscriptionID)
		}
	}

	return nil
}

// azureRMNormalizeLocation is a function which normalises human-readable region/location
// names (e.g. "West US") to the values used and returned by the Azure API (e.g. "westus").
// In state we track the API internal version as it is easier to go from the human form
// to the canonical form than the other way around.
func azureRMNormalizeLocation(location interface{}) string {
	input := location.(string)
	return strings.Replace(strings.ToLower(input), " ", "", -1)
}

// pollIndefinitelyAsNeeded is a terrible hack which is necessary because the Azure
// Storage API (and perhaps others) can have response times way beyond the default
// retry timeouts, with no apparent upper bound. This effectively causes the client
// to continue polling when it reaches the configured timeout. My investigations
// suggest that this is neccesary when deleting and recreating a storage account with
// the same name in a short (though undetermined) time period.
//
// It is possible that this will give Terraform the appearance of being slow in
// future: I have attempted to mitigate this by logging whenever this happens. We
// may want to revisit this with configurable timeouts in the future as clearly
// unbounded wait loops is not ideal. It does seem preferable to the current situation
// where our polling loop will time out _with an operation in progress_, but no ID
// for the resource - so the state will not know about it, and conflicts will occur
// on the next run.
func pollIndefinitelyAsNeeded(client autorest.Client, response *http.Response, acceptableCodes ...int) (*http.Response, error) {
	var resp *http.Response
	var err error

	for {
		resp, err = client.PollAsNeeded(response, acceptableCodes...)
		if err != nil {
			if resp.StatusCode != http.StatusAccepted {
				log.Printf("[DEBUG] Starting new polling loop for %q", response.Request.URL.Path)
				continue
			}

			return resp, err
		}

		return resp, nil
	}
}

// armMutexKV is the instance of MutexKV for ARM resources
var armMutexKV = mutexkv.NewMutexKV()
