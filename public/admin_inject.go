package public

import "bytes"

func injectAdminFleetReportSettingsLink(content []byte) []byte {
	if bytes.Contains(content, []byte("fleet-report-settings-nav-injector")) ||
		!bytes.Contains(content, []byte("</body>")) {
		return content
	}
	return bytes.Replace(content, []byte("</body>"), []byte(adminFleetReportSettingsLinkScript+"</body>"), 1)
}

const adminFleetReportSettingsLinkScript = `<script id="fleet-report-settings-nav-injector">
(function () {
  var targetPath = "/admin/notification/fleet-report-settings";
  var targetLabel = "Fleet Report";

  function pathOf(anchor) {
    try {
      return new URL(anchor.getAttribute("href") || anchor.href || "", window.location.origin).pathname;
    } catch (error) {
      return "";
    }
  }

  function setNodeLabel(node) {
    var candidates = Array.prototype.slice.call(node.querySelectorAll("span, p, div"));
    var textNode = candidates.find(function (item) {
      return item.textContent && item.textContent.trim() === "Traffic Report";
    }) || candidates.find(function (item) {
      return item.textContent && item.textContent.trim() && item.children.length === 0;
    });
    if (textNode) {
      textNode.textContent = targetLabel;
    } else {
      node.appendChild(document.createTextNode(targetLabel));
    }
  }

  function cleanActiveState(node) {
    node.removeAttribute("aria-current");
    node.removeAttribute("data-active");
    node.classList.remove("active", "is-active");
    Array.prototype.forEach.call(node.querySelectorAll("[aria-current], [data-active]"), function (child) {
      child.removeAttribute("aria-current");
      child.removeAttribute("data-active");
      child.classList.remove("active", "is-active");
    });
  }

  function cloneFrom(anchor) {
    var clone = anchor.cloneNode(true);
    clone.setAttribute("href", targetPath);
    cleanActiveState(clone);
    setNodeLabel(clone);
    return clone;
  }

  function makePlainLink() {
    var link = document.createElement("a");
    link.href = targetPath;
    link.textContent = targetLabel;
    link.style.display = "flex";
    link.style.alignItems = "center";
    link.style.gap = "10px";
    link.style.padding = "8px 12px";
    link.style.margin = "2px 0";
    link.style.borderRadius = "8px";
    link.style.color = "inherit";
    link.style.textDecoration = "none";
    return link;
  }

  function ensureLink() {
    if (document.querySelector('a[href="' + targetPath + '"]')) return;
    var anchors = Array.prototype.slice.call(document.querySelectorAll("a"));
    var trafficLink = anchors.find(function (anchor) {
      return pathOf(anchor) === "/admin/notification/traffic-report" ||
        (anchor.textContent || "").trim() === "Traffic Report";
    });
    if (trafficLink && trafficLink.parentNode) {
      trafficLink.parentNode.insertBefore(cloneFrom(trafficLink), trafficLink.nextSibling);
      return;
    }

    var notificationLink = anchors.find(function (anchor) {
      return pathOf(anchor).indexOf("/admin/notification") === 0 ||
        (anchor.textContent || "").trim() === "Notification";
    });
    if (notificationLink && notificationLink.parentNode) {
      notificationLink.parentNode.appendChild(makePlainLink());
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", ensureLink);
  } else {
    ensureLink();
  }
  var observer = new MutationObserver(ensureLink);
  observer.observe(document.documentElement, { childList: true, subtree: true });
})();
</script>`
