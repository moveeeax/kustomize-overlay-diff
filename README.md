# kustomize-overlay-diff
Static audit of a kustomize tree: resolves every overlay inheritance chain and reports what each overlay actually changes versus its base, flagging dead patches, shadowed image overrides, missing bases and silently dropped resources. No cluster, no kustomize binary.
