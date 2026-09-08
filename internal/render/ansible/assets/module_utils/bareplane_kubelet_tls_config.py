"""Plan the single reviewed serving-TLS setting without rewriting kubelet YAML."""

import yaml


LIMIT = 65536


class ConfigLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        keys = [self.construct_object(key, deep=deep) for key, _ in node.value]
        if any(not isinstance(key, str) for key in keys) or len(set(keys)) != len(keys):
            raise ValueError('Kubelet configuration contains non-string or duplicate keys')
        return super().construct_mapping(node, deep=deep)


def enable_serving_bootstrap(original):
    if not isinstance(original, bytes) or not 0 < len(original) <= LIMIT or b'\r' in original or not original.endswith(b'\n'):
        raise ValueError('Kubelet configuration must be bounded LF-terminated YAML')
    try:
        tokens = list(yaml.scan(original))
        if any(isinstance(token, (yaml.tokens.AliasToken, yaml.tokens.AnchorToken, yaml.tokens.TagToken)) for token in tokens):
            raise ValueError('Kubelet aliases, anchors and tags are unsupported')
        document = yaml.load(original, Loader=ConfigLoader)
        if not isinstance(document, dict) or document.get('apiVersion') != 'kubelet.config.k8s.io/v1beta1' or document.get('kind') != 'KubeletConfiguration':
            raise ValueError('Unexpected kubelet configuration type')
        if document.get('tlsCertFile') or document.get('tlsPrivateKeyFile') or document.get('readOnlyPort', 0) != 0:
            raise ValueError('Custom serving credentials or an insecure read-only port block automatic transition')
        if document.get('authentication', {}).get('anonymous', {}).get('enabled', True) is not False:
            raise ValueError('Kubelet anonymous authentication must already be disabled')
        if document.get('authorization', {}).get('mode') != 'Webhook':
            raise ValueError('Kubelet webhook authorization must already be configured')
        enabled = document.get('serverTLSBootstrap', False)
        if type(enabled) is not bool:
            raise ValueError('Kubelet serving bootstrap must be a boolean')
        if 'serverTLSBootstrap' not in document:
            target = original + b'serverTLSBootstrap: true\n'
        else:
            expected = b'serverTLSBootstrap: ' + (b'true' if enabled else b'false') + b'\n'
            lines = original.splitlines(keepends=True)
            if lines.count(expected) != 1:
                raise ValueError('Kubelet serving setting must be a single plain top-level boolean')
            target = b''.join(b'serverTLSBootstrap: true\n' if line == expected else line for line in lines)
        changed = yaml.load(target, Loader=ConfigLoader)
        if changed != dict(document, serverTLSBootstrap=True):
            raise ValueError('Serving transition would modify unrelated kubelet configuration')
        if len(target) > LIMIT:
            raise ValueError('Updated kubelet configuration exceeds its size limit')
        return target
    except (yaml.YAMLError, UnicodeError, TypeError, AttributeError, RecursionError):
        raise ValueError('Cannot safely parse the existing kubelet configuration') from None
