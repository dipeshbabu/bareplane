#!/usr/bin/python
"""Publish the private operator kubeconfig using shared identity validation."""
from ansible.module_utils.bareplane_kubeconfig import main

if __name__ == '__main__':
    main()
