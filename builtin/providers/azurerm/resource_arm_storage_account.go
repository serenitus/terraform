package azurerm

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/arm/storage"
	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/schema"
)

func resourceArmStorageAccount() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmStorageAccountCreate,
		Read:   resourceArmStorageAccountRead,
		Update: resourceArmStorageAccountUpdate,
		Delete: resourceArmStorageAccountDelete,

		Schema: map[string]*schema.Schema{
			"name": &schema.Schema{
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validateArmStorageAccountName,
			},

			"resource_group_name": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"location": &schema.Schema{
				Type:      schema.TypeString,
				Required:  true,
				ForceNew:  true,
				StateFunc: azureRMNormalizeLocation,
			},

			"account_type": &schema.Schema{
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validateArmStorageAccountType,
			},

			"custom_domain": &schema.Schema{
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": &schema.Schema{
							Type:     schema.TypeString,
							Required: true,
						},
						"use_subdomain": &schema.Schema{
							Type:     schema.TypeBool,
							Optional: true,
						},
					},
				},
				Set: func(v interface{}) int {
					var buf bytes.Buffer
					m := v.(map[string]interface{})

					if v, ok := m["name"]; ok {
						buf.WriteString(fmt.Sprintf("%s-", v.(string)))
					}

					if v, ok := m["use_subdomain"]; ok {
						buf.WriteString(fmt.Sprintf("%t-", v.(bool)))
					}

					return hashcode.String(buf.String())
				},
			},

			"primary_location": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_location": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"primary_blob_endpoint": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_blob_endpoint": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"primary_queue_endpoint": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_queue_endpoint": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"primary_table_endpoint": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"secondary_table_endpoint": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			// NOTE: The API does not appear to expose a secondary file endpoint
			"primary_file_endpoint": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"tags": tagsSchema(),
		},
	}
}

func resourceArmStorageAccountCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).storageServiceClient

	resourceGroupName := d.Get("resource_group_name").(string)
	storageAccountName := d.Get("name").(string)
	accountType := d.Get("account_type").(string)
	location := d.Get("location").(string)
	tags := d.Get("tags").(map[string]interface{})

	opts := storage.AccountCreateParameters{
		Location: &location,
		Properties: &storage.AccountPropertiesCreateParameters{
			AccountType: storage.AccountType(accountType),
		},
		Tags: expandTags(tags),
	}

	accResp, err := client.Create(resourceGroupName, storageAccountName, opts)
	if err != nil {
		return fmt.Errorf("Error creating Azure Storage Account '%s': %s", storageAccountName, err)
	}
	_, err = pollIndefinitelyAsNeeded(client.Client, accResp.Response.Response, http.StatusOK)
	if err != nil {
		return fmt.Errorf("Error creating Azure Storage Account %q: %s", storageAccountName, err)
	}

	// The only way to get the ID back apparently is to read the resource again
	account, err := client.GetProperties(resourceGroupName, storageAccountName)
	if err != nil {
		return fmt.Errorf("Error retrieving Azure Storage Account %q: %s", storageAccountName, err)
	}

	d.SetId(*account.ID)

	return resourceArmStorageAccountRead(d, meta)
}

// resourceArmStorageAccountUpdate is unusual in the ARM API where most resources have a combined
// and idempotent operation for CreateOrUpdate. In particular updating all of the parameters
// available requires a call to Update per parameter...
func resourceArmStorageAccountUpdate(d *schema.ResourceData, meta interface{}) error {
	return nil
}

func resourceArmStorageAccountRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).storageServiceClient

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	name := id.Path["storageAccounts"]
	resGroup := id.ResourceGroup

	resp, err := client.GetProperties(resGroup, name)
	if err != nil {
		if resp.StatusCode == http.StatusNoContent {
			d.SetId("")
			return nil
		}

		return fmt.Errorf("Error reading the state of AzureRM Storage Account %q: %s", name, err)
	}

	d.Set("location", resp.Location)
	d.Set("account_type", resp.Properties.AccountType)
	d.Set("primary_location", resp.Properties.PrimaryLocation)
	d.Set("secondary_location", resp.Properties.SecondaryLocation)

	if resp.Properties.PrimaryEndpoints != nil {
		d.Set("primary_blob_endpoint", resp.Properties.PrimaryEndpoints.Blob)
		d.Set("primary_queue_endpoint", resp.Properties.PrimaryEndpoints.Queue)
		d.Set("primary_table_endpoint", resp.Properties.PrimaryEndpoints.Table)
		d.Set("primary_file_endpoint", resp.Properties.PrimaryEndpoints.File)
	}

	if resp.Properties.SecondaryLocation != nil {
		d.Set("secondary_blob_endpoint", resp.Properties.SecondaryEndpoints.Blob)
		d.Set("secondary_queue_endpoint", resp.Properties.SecondaryEndpoints.Queue)
		d.Set("secondary_table_endpoint", resp.Properties.SecondaryEndpoints.Table)
	}

	if resp.Properties.CustomDomain != nil {
		customDomain := map[string]interface{}{
			"name":          resp.Properties.CustomDomain.Name,
			"use_subdomain": resp.Properties.CustomDomain.UseSubDomain,
		}
		d.Set("custom_domain", customDomain)
	}

	flattenAndSetTags(d, resp.Tags)

	return nil
}

func resourceArmStorageAccountDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).storageServiceClient

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	name := id.Path["storageAccounts"]
	resGroup := id.ResourceGroup

	_, err = client.Delete(resGroup, name)
	if err != nil {
		return fmt.Errorf("Error issuing AzureRM delete request for storage account %q: %s", name, err)
	}

	return nil
}

func validateArmStorageAccountName(v interface{}, k string) (ws []string, es []error) {
	input := v.(string)

	if !regexp.MustCompile(`\A([a-z0-9]{3,24})\z`).MatchString(input) {
		es = append(es, fmt.Errorf("name can only consist of lowercase letters and numbers, and must be between 3 and 24 characters long"))
	}

	return
}

func validateArmStorageAccountType(v interface{}, k string) (ws []string, es []error) {
	validAccountTypes := []string{"standard_lrs", "standard_zrs",
		"standard_grs", "standard_ragrs", "premium_lrs"}

	input := strings.ToLower(v.(string))

	for _, valid := range validAccountTypes {
		if valid == input {
			return
		}
	}

	es = append(es, fmt.Errorf("Invalid storage account type %q", input))
	return
}
