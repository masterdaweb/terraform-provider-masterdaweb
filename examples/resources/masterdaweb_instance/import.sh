# By id, or by a name only it has. Its image, project and subscription are read
# back; what the API does not show (its SSH keys, user data, app inputs,
# password) is taken from the configuration on the next apply, without
# reinstalling or resetting anything.
terraform import masterdaweb_instance.web 4123
terraform import masterdaweb_instance.web web-1
