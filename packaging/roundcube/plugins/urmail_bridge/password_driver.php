<?php
/** Native Roundcube password driver; installed as password/drivers/urmail.php. */
class rcube_urmail_password
{
    public function save($current_password, $new_password, $username)
    {
        $plugin = rcmail::get_instance()->plugins->get_plugin('urmail_bridge');
        if (!$plugin instanceof urmail_bridge) {
            return PASSWORD_ERROR;
        }
        return $plugin->save_password($current_password, $new_password, $username);
    }
}
