package web

import (
	"html/template"
	"strings"
)

// bookmarkletJS is the single-function JavaScript the user installs in their
// browser's bookmarks bar. When clicked on a Fastenal product page it:
//
//  1. Extracts product fields from JSON-LD, meta tags, and the URL.
//  2. Opens http://127.0.0.1:8787/ingest?data=<JSON> in a new tab.
//
// Why window.open instead of fetch: Chrome blocks cross-protocol fetches
// from an HTTPS page (fastenal.com) to an HTTP endpoint (127.0.0.1:8787)
// as "mixed content". Navigation via window.open is allowed, and our
// server handles the GET with the payload in a URL query param, returning
// a tiny confirmation page that auto-closes.
//
// Keep this compact — every byte is in the user's bookmarks bar.
const bookmarkletJS = `(function(){
var d={};var diag={};
/* Path 1: JSON-LD */
document.querySelectorAll('script[type="application/ld+json"]').forEach(function(s){
  diag.ld=(diag.ld||0)+1;
  try{
    var j=JSON.parse(s.textContent);
    var items=Array.isArray(j)?j:(j['@graph']||[j]);
    items.forEach(function(it){
      if(!it||!it['@type'])return;
      var t=Array.isArray(it['@type'])?it['@type'][0]:it['@type'];
      if(t!=='Product')return;
      d.name=d.name||it.name;
      d.sku=d.sku||it.sku||it.mpn;
      d.description=d.description||it.description;
      if(it.brand){d.manufacturer=d.manufacturer||(typeof it.brand==='string'?it.brand:it.brand.name);}
      var offer=Array.isArray(it.offers)?it.offers[0]:it.offers;
      if(offer){
        if(offer.price!=null)d.price=d.price||parseFloat(offer.price);
        d.currency=d.currency||offer.priceCurrency;
      }
    });
  }catch(e){}
});
/* Path 2: og:/product: meta tags */
function meta(p){var e=document.querySelector('meta[property="'+p+'"]')||document.querySelector('meta[name="'+p+'"]');return e?e.content:'';}
d.name=d.name||meta('og:title');
d.description=d.description||meta('og:description');
if(!d.price){var mp=meta('product:price:amount');if(mp)d.price=parseFloat(mp);}
d.currency=d.currency||meta('product:price:currency');
/* Path 3: DOM/innerText fallbacks (Fastenal doesn't ship structured data) */
var txt=document.body.innerText||'';
if(!d.description){var h1=document.querySelector('h1');if(h1)d.description=h1.textContent.trim();}
if(!d.description)d.description=document.title;
if(!d.sku){var sm=txt.match(/(?:Item|Fastenal|Stock)\s*(?:#|No\.?|Number)[\s:]*([A-Za-z0-9-]+)/i);if(sm)d.sku=sm[1];}
if(!d.sku){var qm=location.href.match(/[?&]query=([^&#]+)/);if(qm)d.sku=decodeURIComponent(qm[1]);}
if(!d.sku){var pm=location.pathname.match(/\/details?\/(\w+)/i);if(pm)d.sku=pm[1];}
if(!d.price){var $m=txt.match(/\$\s*([\d,]+\.\d{2})/);if($m)d.price=parseFloat($m[1].replace(/,/g,''));}
if(!d.manufacturer){var mm=txt.match(/(?:Manufacturer|Brand|Mfr\.?)[\s:]*([A-Z][A-Za-z0-9 .&'-]{1,60})/);if(mm)d.manufacturer=mm[1].trim();}
if(!d.manufacturer)d.manufacturer='Fastenal';
if(!d.package){var km=txt.match(/(?:Package|Pack\s*Qty|Unit\s*of\s*Measure)[\s:]*([A-Za-z0-9 \/]{1,30})/i);if(km)d.package=km[1].trim();}
d.currency=d.currency||'USD';
d.url=location.href;
d._diag=diag;
var payload=encodeURIComponent(JSON.stringify(d));
var w=window.open('http://127.0.0.1:8787/ingest?data='+payload,'fusion_enrich');
if(!w){alert('Popup blocked — allow popups for fastenal.com or click the bookmarklet again.');}
})();`

// bookmarkletAnchor returns the full `<a>` element as template.HTML so it
// bypasses html/template's URL sanitization entirely. Using template.URL
// alone isn't enough — Go's sanitizer still rewrites javascript: URLs
// to #ZgotmplZ in some cases even with that type. template.HTML is the
// escape hatch: it inserts the markup verbatim.
//
// We stringify the JS body, strip whitespace, and only escape `%` (so the
// browser's URL parser doesn't treat a literal `%` as an escape prefix)
// and `"` (so it doesn't terminate the href attribute early). Single
// quotes inside the JS are safe because the attribute is double-quoted.
func bookmarkletAnchor(label, className string) template.HTML {
	r := strings.NewReplacer(
		"\n", "", "\r", "", "\t", "",
		"%", "%25",
		`"`, "%22",
	)
	href := "javascript:" + r.Replace(bookmarkletJS)
	return template.HTML(
		`<a class="` + className + `" href="` + href +
			`" onclick="event.preventDefault(); alert('Drag this link to your bookmarks bar instead of clicking it.\n\nOnce installed, open any Fastenal product page and click the bookmark to capture its fields back into this app.');">` +
			label +
			`</a>`)
}
