<?php
class urmail_bridge_error extends RuntimeException {}
class urmail_bridge_remote_error extends urmail_bridge_error
{
    public $http_status;
    public function __construct($message, $http_status)
    {
        parent::__construct($message);
        $this->http_status = $http_status;
    }
}

/** Server-side mailbox principal bridge. No panel SSO or mailbox password export. */
class urmail_bridge extends rcube_plugin
{
    public $task = 'mail|settings|logout|login';

    public function init()
    {
        $this->add_hook('login_after', [$this, 'login']);
        $this->add_hook('session_destroy', [$this, 'logout']);
        $this->add_hook('password_change', [$this, 'password_changed']);
        if (rcmail::get_instance()->task === 'login' && (!empty($_GET['_urmail_password_changed']) || !empty($_GET['_urmail_password_unknown']))) {
            $unknown = !empty($_GET['_urmail_password_unknown']);
            rcmail::get_instance()->output->command('display_message', $this->text($unknown ? 'page.webmail.password_unknown' : 'page.webmail.password_relogin'), $unknown ? 'warning' : 'confirmation');
        }
        $this->register_action('plugin.urmail_context', [$this, 'context']);
        $this->register_action('plugin.urmail_sieve', [$this, 'sieve_ticket']);
        $this->include_script('bridge.js');
        $labels = [];
        foreach (['classify', 'summarize', 'translate', 'phishing', 'polish', 'sieve', 'language', 'select_message', 'suggestion'] as $operation) {
            $labels[$operation] = $this->text('page.webmail.' . $operation);
        }
        $labels['cloud_confirm'] = $this->text('page.webmail.cloud_confirm');
        $labels['cloud_scope'] = $this->text('page.ai.cloud_scope');
        $labels['authorization_unavailable'] = $this->text('page.webmail.authorization_unavailable');
        $policy = ['allowed' => false];
        if (!empty($_SESSION['urmail_mailbox_session'])) {
            try { $policy = $this->call_bridge(['action' => 'ai_policy', 'session' => $_SESSION['urmail_mailbox_session']]); }
            catch (Throwable $error) { /* The mailbox remains usable while AI is unavailable. */ }
        }
        rcmail::get_instance()->output->set_env('urmail_ai_policy', $policy);
        rcmail::get_instance()->output->set_env('urmail_labels', $labels);
        foreach (['classify', 'summarize', 'translate', 'phishing', 'polish', 'sieve'] as $operation) {
            $this->add_button(['type' => 'link', 'command' => 'plugin.urmail_' . $operation,
                'class' => 'button urmail-action', 'content' => htmlspecialchars($labels[$operation], ENT_QUOTES, 'UTF-8')], 'toolbar');
        }
    }

    private function call_bridge($payload)
    {
        $rc = rcmail::get_instance();
        $key = $rc->config->get('urmail_bridge_key', '');
        $url = $rc->config->get('urmail_bridge_url', '');
        // Never let tenant preferences redirect credentials to another endpoint.
        if (strlen($key) < 32 || $url !== 'http://127.0.0.1:8080/api/v1/roundcube/bridge') {
            throw new RuntimeException('Mailbox bridge disabled');
        }
        $body = json_encode($payload, JSON_UNESCAPED_SLASHES | JSON_THROW_ON_ERROR);
        $time = (string) time();
        $nonce = bin2hex(random_bytes(32));
        $signature = hash_hmac('sha256', "POST\n/api/v1/roundcube/bridge\n$time\n$nonce\n" . $body, $key);
        $curl = curl_init($url);
        curl_setopt_array($curl, [CURLOPT_POST => true, CURLOPT_POSTFIELDS => $body,
            CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => 5, CURLOPT_CONNECTTIMEOUT => 2,
            CURLOPT_FOLLOWLOCATION => false, CURLOPT_PROXY => '', CURLOPT_PROTOCOLS => CURLPROTO_HTTP,
            CURLOPT_HTTPHEADER => ['Content-Type: application/json', "X-Urmail-Timestamp: $time",
                "X-Urmail-Nonce: $nonce", "X-Urmail-Signature: $signature"]]);
        $result = curl_exec($curl);
        $status = curl_getinfo($curl, CURLINFO_RESPONSE_CODE);
        curl_close($curl);
        if ($status !== 200 || $result === false) {
            if (is_string($result)) {
                $failure = json_decode($result, true);
                $code = $failure['error']['code'] ?? '';
                if (is_string($code) && preg_match('/^[A-Z0-9_]+$/D', $code)) {
                    throw new urmail_bridge_remote_error($this->text('error.' . $code), $status);
                }
            }
            throw new urmail_bridge_error($this->text('page.webmail.authorization_unavailable'));
        }
        return json_decode($result, true, 32, JSON_THROW_ON_ERROR);
    }

    public function login($args)
    {
        $rc = rcmail::get_instance();
        // This hook is executed only after Roundcube authenticated IMAP login.
        unset($_SESSION['urmail_mailbox_session']);
        try {
            if (!$rc->get_storage()->check_connection()) {
                return $args;
            }
            $result = $this->call_bridge(['action' => 'login', 'address' => $rc->user->get_username()]);
            $_SESSION['urmail_mailbox_session'] = $result['session'];
        } catch (Throwable $error) {
            // Independent webmail remains usable when the optional bridge fails.
            rcube::write_log('errors', 'urMail mailbox bridge login unavailable');
        }
        return $args;
    }

    // Called only by the native password plugin's installed driver.
    public function save_password($old_password, $new_password, $username)
    {
        $rc = rcmail::get_instance();
        if ($_SERVER['REQUEST_METHOD'] !== 'POST' || !$rc->check_request()
            || $rc->task !== 'settings' || $rc->action !== 'plugin.password-save'
            || !$rc->get_storage()->check_connection()
            || empty($_SESSION['urmail_mailbox_session'])
            || !is_string($username) || $username !== $rc->user->get_username()) {
            return PASSWORD_ERROR;
        }
        if (!is_string($old_password) || !is_string($new_password) || strlen($old_password) < 1
            || strlen($old_password) > 4096 || strlen($new_password) > 4096 || $old_password === $new_password
            || !preg_match('//u', $new_password) || preg_match('/[\p{Cc}]/u', $new_password)
            || ($characters = preg_match_all('/./us', $new_password)) < 8 || $characters > 1024) {
            return PASSWORD_CONSTRAINT_VIOLATION;
        }
        try {
            $result = $this->call_bridge(['action' => 'change_password',
                'session' => $_SESSION['urmail_mailbox_session'], 'address' => $rc->user->get_username(),
                'old_password' => $old_password, 'new_password' => $new_password]);
            if (empty($result['changed']) || empty($result['reauthentication_required'])) {
                $this->password_relogin(true);
            }
            $_SESSION['urmail_password_changed'] = true;
            return PASSWORD_SUCCESS;
        } catch (urmail_bridge_remote_error $error) {
            if ($error->http_status >= 500) {
                $this->password_relogin(true);
            }
            return PASSWORD_ERROR;
        } catch (Throwable $error) {
            // A lost response may follow a committed change. Require authentication
            // again without guessing whether the old or new credential is current.
            $this->password_relogin(true);
        }
    }

    public function password_changed($args)
    {
        if (empty($_SESSION['urmail_password_changed'])) {
            return $args;
        }
        $this->password_relogin(false);
    }

    private function password_relogin($uncertain)
    {
        unset($_SESSION['urmail_password_changed'], $_SESSION['urmail_mailbox_session']);
        $rc = rcmail::get_instance();
        // The core password plugin would otherwise cache the new password after
        // this hook. Terminate the request after the server-side session dies.
        $rc->kill_session();
        header('Cache-Control: no-store');
        header('Location: ./?_task=login&' . ($uncertain ? '_urmail_password_unknown=1' : '_urmail_password_changed=1'), true, 303);
        exit;
    }

    public function logout($args)
    {
        try {
            if (!empty($_SESSION['urmail_mailbox_session'])) {
                $this->call_bridge(['action' => 'logout', 'session' => $_SESSION['urmail_mailbox_session']]);
            }
        } catch (Throwable $error) {
            rcube::write_log('errors', 'urMail mailbox bridge revocation unavailable');
            // Keep the Roundcube session until durable revocation succeeds.
            // Never acknowledge logout while its authority remains valid.
            throw new RuntimeException($this->text('page.webmail.authorization_unavailable'));
        }
        unset($_SESSION['urmail_mailbox_session']);
        return $args;
    }

    private function text($key)
    {
        $language = rcmail::get_instance()->config->get('language', 'en_US');
        $locale = strpos($language, 'zh') === 0 ? 'zh-CN' : 'en';
        $catalog = json_decode(file_get_contents('/usr/share/urmail/locales/' . $locale . '.json'), true);
        return $catalog[$key] ?? ($catalog['page.webmail.authorization_unavailable'] ?? $key);
    }

    public function context()
    {
        $rc = rcmail::get_instance();
        // Roundcube's authenticated AJAX action validates its own request token.
        if ($_SERVER['REQUEST_METHOD'] !== 'POST' || !$rc->check_request()) {
            http_response_code(403);
            exit;
        }
        $folder = (string) rcube_utils::get_input_value('_mbox', rcube_utils::INPUT_POST);
        $uid = (string) rcube_utils::get_input_value('_uid', rcube_utils::INPUT_POST);
        if (!preg_match('/^[1-9][0-9]{0,9}$/D', $uid) || strlen($folder) > 255 || preg_match('/[\x00\r\n]/', $folder)) {
            http_response_code(400);
            exit;
        }
        $storage = $rc->get_storage();
        if (!$storage->check_connection()) { http_response_code(403); exit; }
        // UID and UIDVALIDITY are resolved from this authenticated IMAP session,
        // never from a panel identity, client message body or a supplied tenant.
        $before = $storage->conn->status($folder, ['UIDVALIDITY']);
        $header = $storage->get_message_headers($uid, $folder, true);
        $data = $storage->conn->status($folder, ['UIDVALIDITY']);
        if (empty($before['UIDVALIDITY']) || $before['UIDVALIDITY'] !== ($data['UIDVALIDITY'] ?? null)) {
            http_response_code(409);
            exit;
        }
        if (!$header || empty($data['UIDVALIDITY']) || empty($_SESSION['urmail_mailbox_session'])) {
            http_response_code(403);
            exit;
        }
        try {
            $policy = $this->call_bridge(['action' => 'ai_policy', 'session' => $_SESSION['urmail_mailbox_session']]);
            $expected_target = (string) rcube_utils::get_input_value('_configuration_digest', rcube_utils::INPUT_POST);
            if (empty($policy['allowed']) || empty($policy['configuration_digest']) || !hash_equals($policy['configuration_digest'], $expected_target)
                || (!empty($policy['requires_confirmation']) && rcube_utils::get_input_value('_cloud_confirmed', rcube_utils::INPUT_POST) !== '1')) {
                throw new urmail_bridge_error($this->text('error.AI_CLOUD_CONSENT_STALE'));
            }
            $claims = ['scope' => 'ai_context', 'folder' => $folder,
                'uid' => (int) $uid, 'uid_validity' => (int) $data['UIDVALIDITY']];
            $issued = $this->call_bridge($claims + ['action' => 'authorize', 'session' => $_SESSION['urmail_mailbox_session']]);
            $operation = (string) rcube_utils::get_input_value('_operation', rcube_utils::INPUT_POST);
            $language = (string) rcube_utils::get_input_value('_language', rcube_utils::INPUT_POST);
            $request_key = (string) rcube_utils::get_input_value('_request_key', rcube_utils::INPUT_POST);
            if (!in_array($operation, ['classify', 'summarize', 'translate', 'phishing', 'polish'], true) || strlen($language) > 64 || strlen($request_key) > 200 || ($header->size ?? 0) > 1048576) {
                throw new urmail_bridge_error($this->text('page.webmail.message_too_large'));
            }
            $message = new rcube_message($uid, $folder);
            $part = null;
            $is_html = false;
            if (!$message->has_text_part($part)) {
                if (!$message->has_html_part(false, $part)) {
                    throw new urmail_bridge_error($this->text('page.webmail.no_text_body'));
                }
                $is_html = true;
            }
            if (($part->size ?? 0) > 65536) { throw new urmail_bridge_error($this->text('page.webmail.message_too_large')); }
            $body = $message->get_part_body($part->mime_id, false, 65537);
            if (!is_string($body) || strlen($body) > 65536) { throw new urmail_bridge_error($this->text('page.webmail.message_too_large')); }
            if ($is_html) { $body = html_entity_decode(strip_tags($body), ENT_QUOTES | ENT_HTML5, 'UTF-8'); }
            $current = $storage->conn->status($folder, ['UIDVALIDITY']);
            if (($current['UIDVALIDITY'] ?? null) !== $data['UIDVALIDITY']) { throw new RuntimeException('Message UID validity changed'); }
            $result = $this->call_bridge($claims + ['action' => 'ai', 'ticket' => $issued['ticket'], 'request_key' => $request_key, 'configuration_digest' => $expected_target,
                'input' => ['operation' => $operation, 'body' => $body, 'subject' => (string) $header->subject,
                    'from' => (string) $header->from, 'target_language' => $language]]);
            $rc->output->command('plugin.urmail_result', $result['result']);
        } catch (Throwable $error) {
            $rc->output->command('display_message', $error instanceof urmail_bridge_error ? $error->getMessage() : $this->text('page.webmail.authorization_unavailable'), 'error');
        }
        $rc->output->send();
    }
    public function sieve_ticket()
    {
        $rc = rcmail::get_instance();
        if ($_SERVER['REQUEST_METHOD'] !== 'POST' || !$rc->check_request() || !$rc->get_storage()->check_connection() || empty($_SESSION['urmail_mailbox_session'])) {
            http_response_code(403); exit;
        }
        try {
            $issued = $this->call_bridge(['action' => 'authorize', 'scope' => 'sieve', 'session' => $_SESSION['urmail_mailbox_session']]);
            $rc->output->command('plugin.urmail_sieve_ticket', ['ticket' => $issued['ticket']]);
        } catch (Throwable $error) {
            $rc->output->command('display_message', $error instanceof urmail_bridge_error ? $error->getMessage() : $this->text('page.webmail.authorization_unavailable'), 'error');
        }
        $rc->output->send();
    }

}
