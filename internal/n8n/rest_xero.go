package n8n

import (
	"net/http"
	"strconv"
)

// Xero (n8n-nodes-base.xero), Accounting API 2.0. The tenant comes from
// the organizationId parameter (Xero-tenant-id header).

const (
	xeroInvoiceFields = "brandingThemeId:BrandingThemeID currency:CurrencyCode currencyRate:CurrencyRate date:Date dueDate:DueDate expectedPaymentDate:ExpectedPaymentDate " +
		"invoiceNumber:InvoiceNumber lineAmountType:LineAmountTypes plannedPaymentDate:PlannedPaymentDate reference:Reference sendToContact:SentToContact status:Status url:Url"
	xeroContactFields = "accountNumber:AccountNumber bankAccountDetails:BankAccountDetails contactNumber:ContactNumber contactStatus:ContactStatus defaultCurrency:DefaultCurrency " +
		"emailAddress:EmailAddress firstName:FirstName lastName:LastName purchasesDefaultAccountCode:PurchasesDefaultAccountCode salesDefaultAccountCode:SalesDefaultAccountCode " +
		"skypeUserName:SkypeUserName taxNumber xeroNetworkKey"
)

func init() {
	registerREST("xero", &restService{
		defResource: "invoice",
		defOps:      map[string]string{"invoice": "create", "contact": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("xeroOAuth2Api")
			if err != nil {
				return nil, err
			}
			restDefaultLimit(ec, 100)
			return bearerClient(ec.apiBase("xero", "https://api.xero.com/api.xro/2.0"), c), nil
		},
		pager: xeroPager,
		ops: map[string]*restOp{
			"invoice/create": {method: "POST", path: "/Invoices", out: "Invoices", body: "type:Type contactId:Contact.ContactID @additionalFields " + xeroInvoiceFields,
				build: xeroBuild("lineItemsUi", "")},
			"invoice/update": {method: "POST", path: "/Invoices/{invoiceId}", out: "Invoices", body: "@updateFields type:Type " + xeroInvoiceFields,
				build: xeroBuild("updateFields.lineItemsUi", "")},
			"invoice/get":    {path: "/Invoices/{invoiceId}", out: "Invoices", build: xeroBuild("", "")},
			"invoice/getAll": {path: "/Invoices", list: true, listKey: "Invoices", build: xeroListQuery("statuses")},
			"contact/create": {method: "POST", path: "/Contacts", out: "Contacts", body: "name:Name @additionalFields " + xeroContactFields,
				build: xeroBuild("", "additionalFields")},
			"contact/update": {method: "POST", path: "/Contacts/{contactId}", out: "Contacts", body: "@updateFields name:Name " + xeroContactFields,
				build: xeroBuild("", "updateFields")},
			"contact/get":    {path: "/Contacts/{contactId}", out: "Contacts", build: xeroBuild("", "")},
			"contact/getAll": {path: "/Contacts", list: true, listKey: "Contacts", build: xeroListQuery("includeArchived")},
		},
	})
}

// xeroPager pages with page=1,2,… until an empty page (returnAll only; a
// limited call reads one page as n8n does).
func xeroPager(p *restPage, _ interface{}, _ http.Header) bool {
	if p.query.Get("page") == "" {
		return false
	}
	p.query.Set("page", strconv.Itoa(p.n+2))
	return true
}

// xeroBuild sets the tenant header and builds line items (invoice) or
// phones/addresses wrapped in Contacts (contact).
func xeroBuild(lineItems, contactColl string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if org, _ := ec.str(i, "organizationId", ""); org != "" {
			r.headers["Xero-tenant-id"] = org
		}
		if lineItems != "" {
			v, err := ec.param(i, lineItems+".lineItemsValues", nil)
			if err != nil {
				return err
			}
			if !restEmpty(v) {
				var out []interface{}
				for _, x := range asList(v) {
					m := asMap(x)
					li := map[string]interface{}{"Tracking": []interface{}{}}
					for k, f := range map[string]string{"accountCode": "AccountCode", "description": "Description", "discountRate": "DiscountRate", "itemCode": "ItemCode",
						"lineAmount": "LineAmount", "taxAmount": "TaxAmount", "taxType": "TaxType", "unitAmount": "UnitAmount"} {
						if m[k] != nil {
							li[f] = m[k]
						}
					}
					if m["quantity"] != nil {
						li["Quantity"] = toString(m["quantity"])
					}
					out = append(out, li)
				}
				r.body["LineItems"] = out
			}
		}
		if contactColl != "" {
			f := fieldsOf(ec, i, contactColl)
			var phones, addrs []interface{}
			for _, x := range asList(asMap(f["phonesUi"])["phonesValues"]) {
				m := asMap(x)
				phones = append(phones, map[string]interface{}{"PhoneType": m["phoneType"], "PhoneNumber": m["phoneNumber"], "PhoneAreaCode": m["phoneAreaCode"], "PhoneCountryCode": m["phoneCountryCode"]})
			}
			for _, x := range asList(asMap(f["addressesUi"])["addressesValues"]) {
				m := asMap(x)
				addrs = append(addrs, map[string]interface{}{"AddressType": m["type"], "AddressLine1": m["line1"], "AddressLine2": m["line2"], "City": m["city"],
					"Region": m["region"], "PostalCode": m["postalCode"], "Country": m["country"], "AttentionTo": m["attentionTo"]})
			}
			if phones != nil {
				r.body["Phones"] = phones
			}
			if addrs != nil {
				r.body["Addresses"] = addrs
			}
			r.body = map[string]interface{}{"Contacts": []interface{}{r.body}}
		}
		return nil
	}
}

// xeroListQuery maps the getAll options; returnAll starts paging at page 1.
func xeroListQuery(flag string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if err := xeroBuild("", "")(ec, i, r); err != nil {
			return err
		}
		o := fieldsOf(ec, i, "options")
		if flag == "statuses" {
			var s string
			for k, x := range asListOrOne(o["statuses"]) {
				if k > 0 {
					s += ","
				}
				s += toString(x)
			}
			if s != "" {
				r.query.Set("statuses", s)
			}
		} else if toBool(o[flag]) {
			r.query.Set(flag, "true")
		}
		if toBool(o["createdByMyApp"]) {
			r.query.Set("createdByMyApp", "true")
		}
		if ob := toString(o["orderBy"]); ob != "" && ob != "<nil>" {
			so := toString(o["sortOrder"])
			if so == "" || so == "<nil>" {
				so = "DESC"
			}
			r.query.Set("order", ob+" "+so)
		}
		if w := toString(o["where"]); w != "" && w != "<nil>" {
			r.query.Set("where", w)
		}
		if all, _ := ec.boolean(i, "returnAll", false); all {
			r.query.Set("page", "1")
		}
		return nil
	}
}
